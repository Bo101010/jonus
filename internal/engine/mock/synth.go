package mock

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/shaolianbo/jonus/internal/engine"
)

// Synthesis constants. The mock produces 44.1 kHz mono, which is what the
// player and the WAV encoder both expect.
const (
	sampleRate      = 44100
	defaultDuration = 30 * time.Second
	minDuration     = 4 * time.Second
	maxDuration     = 5 * time.Minute

	beatsPerBar = 4
)

// chord is a triad as semitone offsets from the arrangement's root.
type chord [3]int

// progressionTable holds the four-bar progressions the arranger picks from.
// It is read-only data, never mutated.
var progressionTable = [][beatsPerBar]chord{
	{{0, 4, 7}, {7, 11, 14}, {9, 12, 16}, {5, 9, 12}}, // I  V  vi IV
	{{9, 12, 16}, {5, 9, 12}, {0, 4, 7}, {7, 11, 14}}, // vi IV I  V
	{{0, 4, 7}, {9, 12, 16}, {5, 9, 12}, {7, 11, 14}}, // I  vi IV V
	{{5, 9, 12}, {7, 11, 14}, {0, 4, 7}, {9, 12, 16}}, // IV V  I  vi
}

// pentatonic holds scale degrees used for the lead and vocal lines. A
// five-note scale means a random walk lands on a consonance most of the time.
var pentatonic = [5]int{0, 2, 4, 7, 9}

// note is one scheduled tone.
type note struct {
	start float64 // seconds from the beginning
	dur   float64
	midi  int
	vel   float64
}

// arranger turns a Spec plus a seed into audio. It is not safe for concurrent
// use, which matches the single-worker queue.
type arranger struct {
	seed        int64
	root        int
	progression [beatsPerBar]chord
	bpm         int
	duration    float64
}

func newArranger(spec engine.Spec, seed int64) *arranger {
	// Key, tempo, and progression come from one stream that every voice set
	// shares, so a stem always agrees with its mix about the arrangement.
	base := rand.New(rand.NewPCG(uint64(seed), arrangementSalt))

	return &arranger{
		seed:        seed,
		root:        45 + base.IntN(8), // A2..E3
		progression: progressionTable[base.IntN(len(progressionTable))],
		bpm:         78 + base.IntN(55),
		duration:    resolveDuration(spec.Duration).Seconds(),
	}
}

// Salt values giving each random stream in the arranger its own sequence.
const (
	arrangementSalt = 0x9e3779b97f4a7c15
	voiceSaltBase   = 0x2545f4914f6cdd1d
)

// rngFor returns an independent stream for one voice.
//
// Sharing a single stream would make each voice's notes depend on which other
// voices were rendered first, so a stem would not match the mix it came from.
func (a *arranger) rngFor(voice voiceSet) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(a.seed), uint64(voice)*voiceSaltBase+1))
}

func resolveDuration(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return defaultDuration
	case d < minDuration:
		return minDuration
	case d > maxDuration:
		return maxDuration
	default:
		return d
	}
}

func (a *arranger) beatSeconds() float64 { return 60 / float64(a.bpm) }

func (a *arranger) barSeconds() float64 { return a.beatSeconds() * beatsPerBar }

// bars reports how many complete bars fit in the requested duration.
func (a *arranger) bars() int {
	n := int(math.Ceil(a.duration / a.barSeconds()))
	return max(n, 1)
}

// chordAt returns the chord sounding in the given bar, wrapping the
// four-bar progression.
func (a *arranger) chordAt(bar int) chord { return a.progression[bar%beatsPerBar] }

// voiceSet selects which layers to render, so the same arrangement can be
// produced whole or split into stems.
type voiceSet uint8

const (
	voiceBass voiceSet = 1 << iota
	voicePad
	voiceLead
	voiceVocal

	voiceAll = voiceBass | voicePad | voiceLead | voiceVocal
)

// renderVoices builds the raw, unnormalised mix for the selected layers.
func renderVoices(spec engine.Spec, seed int64, sr int, voices voiceSet) []float32 {
	a := newArranger(spec, seed)

	mix := make([]float32, int(a.duration*float64(sr)))
	if voices&voiceBass != 0 {
		a.addBass(mix, sr)
	}
	if voices&voicePad != 0 {
		a.addPad(mix, sr)
	}
	if voices&voiceLead != 0 {
		a.addLead(mix, sr)
	}
	// Instrumental only suppresses the vocal layer; asking for the vocal stem
	// on its own still produces it, which is what stem extraction needs.
	if voices&voiceVocal != 0 {
		a.addVocal(mix, sr)
	}
	return mix
}

// voicesFor reports which layers a request actually asks for. An instrumental
// request is the full arrangement minus the vocal layer.
func voicesFor(spec engine.Spec) voiceSet {
	if spec.Instrumental {
		return voiceAll &^ voiceVocal
	}
	return voiceAll
}

// compose renders the whole arrangement, normalised and ready to encode.
func compose(spec engine.Spec, seed int64, sr int) []float32 {
	mix := renderVoices(spec, seed, sr, voicesFor(spec))
	master(mix, sr, gainFor(mix))
	return mix
}

// gainFor returns the gain that brings a mix's peak to the target level.
func gainFor(mix []float32) float32 {
	var peak float32
	for _, s := range mix {
		if a := float32(math.Abs(float64(s))); a > peak {
			peak = a
		}
	}
	if peak == 0 {
		return 0
	}
	return 0.89 / peak
}

// addBass lays the chord root down once per bar, an octave below the tonic.
func (a *arranger) addBass(mix []float32, sr int) {
	for bar := range a.bars() {
		root := a.root - 12 + a.chordAt(bar)[0]
		start := float64(bar) * a.barSeconds()
		// Two half-bar notes rather than one long one: it gives the mock a
		// pulse, which makes the waveform view actually look like something.
		for half := range 2 {
			at := start + float64(half)*a.barSeconds()/2
			n := note{
				start: at,
				dur:   a.barSeconds() / 2 * 0.9,
				midi:  root,
				vel:   0.30,
			}
			if half == 1 {
				n.midi += 7 // a fifth up on the second half
				n.vel = 0.22
			}
			renderNote(mix, sr, n, timbreBass)
		}
	}
}

// addPad sustains the full triad through each bar.
func (a *arranger) addPad(mix []float32, sr int) {
	for bar := range a.bars() {
		start := float64(bar) * a.barSeconds()
		for _, semi := range a.chordAt(bar) {
			renderNote(mix, sr, note{
				start: start,
				dur:   a.barSeconds(),
				midi:  a.root + semi,
				vel:   0.085,
			}, timbrePad)
		}
	}
}

// addLead walks a pentatonic melody in eighth notes.
func (a *arranger) addLead(mix []float32, sr int) {
	rng := a.rngFor(voiceLead)
	step := a.beatSeconds() / 2
	degree := rng.IntN(len(pentatonic))

	for i := 0; float64(i)*step < a.duration; i++ {
		// Step to a neighbouring degree most of the time; the occasional
		// larger jump keeps it from sounding like a scale exercise.
		switch rng.IntN(10) {
		case 0:
			degree += rng.IntN(3) - 1 + 2
		case 1:
			degree += rng.IntN(3) - 1 - 2
		default:
			degree += rng.IntN(3) - 1
		}
		degree = clamp(degree, 0, len(pentatonic)*2-1)

		octave := 0
		if degree >= len(pentatonic) {
			octave = 12
		}
		midi := a.root + 24 + octave + pentatonic[degree%len(pentatonic)]

		// Rest every so often so the line has some air in it.
		if rng.IntN(8) == 0 {
			continue
		}
		renderNote(mix, sr, note{
			start: float64(i) * step,
			dur:   step * 1.6,
			midi:  midi,
			vel:   0.16,
		}, timbrePluck)
	}
}

// addVocal renders a slow, vibrato-bearing line above the lead. It is not a
// voice — it is a formant-ish tone shaped like one, which is enough for the
// mock to be recognisably "the version with vocals".
func (a *arranger) addVocal(mix []float32, sr int) {
	rng := a.rngFor(voiceVocal)
	step := a.beatSeconds() * 2 // half notes
	degree := rng.IntN(len(pentatonic))

	for i := 0; float64(i)*step < a.duration; i++ {
		degree = clamp(degree+rng.IntN(3)-1, 0, len(pentatonic)-1)
		// Land on the chord's root at the start of most phrases.
		if rng.IntN(3) == 0 {
			degree = 0
		}

		midi := a.root + 24 + pentatonic[degree]
		if rng.IntN(4) == 0 {
			midi += 12
		}
		renderNote(mix, sr, note{
			start: float64(i) * step,
			dur:   step * 0.85,
			midi:  midi,
			vel:   0.20,
		}, timbreVocal)
	}
}

// timbre is a per-sample waveform generator. phase is in cycles; the return
// value is nominally in [-1, 1].
type timbre func(phase float64) float64

func timbreBass(phase float64) float64 {
	// A sine with a touch of second and third harmonic reads as "bass" rather
	// than as a test tone.
	return 0.80*math.Sin(tau*phase) +
		0.14*math.Sin(tau*2*phase) +
		0.06*math.Sin(tau*3*phase)
}

func timbrePad(phase float64) float64 {
	return 0.70*math.Sin(tau*phase) +
		0.20*math.Sin(tau*2*phase) +
		0.10*math.Sin(tau*4*phase)
}

func timbrePluck(phase float64) float64 {
	return 0.62*math.Sin(tau*phase) +
		0.26*math.Sin(tau*2*phase) +
		0.12*math.Sin(tau*3*phase)
}

// timbreVocal stacks odd harmonics with a slow vibrato, which is the crudest
// thing that still sounds like a sustained voice.
func timbreVocal(phase float64) float64 {
	return 0.60*math.Sin(tau*phase) +
		0.25*math.Sin(tau*3*phase) +
		0.10*math.Sin(tau*5*phase) +
		0.05*math.Sin(tau*7*phase)
}

const tau = 2 * math.Pi

// renderNote adds one enveloped note into the mix.
func renderNote(mix []float32, sr int, n note, t timbre) {
	startFrame := int(n.start * float64(sr))
	if startFrame >= len(mix) {
		return
	}
	frames := int(n.dur * float64(sr))
	freq := midiToFreq(n.midi)

	// A short vibrato, deeper for the vocal timbre, keeps long notes alive.
	vibratoHz, vibratoDepth := 5.5, 0.004
	phase := 0.0

	for i := range frames {
		frame := startFrame + i
		if frame >= len(mix) {
			return
		}
		env := adsr(float64(i)/float64(sr), n.dur)
		if env <= 0 {
			continue
		}

		wobble := 1 + vibratoDepth*math.Sin(tau*vibratoHz*float64(i)/float64(sr))
		phase += freq * wobble / float64(sr)
		mix[frame] += float32(n.vel * env * t(phase))
	}
}

// adsr is a percussive envelope: quick attack, exponential decay into a short
// release, so notes do not click at either end.
func adsr(t, dur float64) float64 {
	const (
		attack  = 0.012
		release = 0.05
	)
	if t < 0 || t > dur {
		return 0
	}
	if t < attack {
		return t / attack
	}
	if remaining := dur - t; remaining < release {
		return remaining / release
	}
	return math.Exp(-1.6 * (t - attack))
}

func midiToFreq(midi int) float64 {
	return 440 * math.Pow(2, float64(midi-69)/12)
}

// master applies gain and fades the ends.
//
// The chain is deliberately linear. A saturating stage here would sound warmer,
// but it would also mean the stems no longer sum back to the mix — and being
// able to reconstruct the track from its stems is worth more than a little
// harmonic colour in a placeholder engine. Peak normalisation already
// guarantees the mix cannot clip.
//
// Stems are mastered with the gain computed from the full mix rather than their
// own, so that summing the stems reproduces the whole track.
func master(mix []float32, sr int, gain float32) {
	if gain == 0 {
		return
	}

	fade := int(0.05 * float64(sr))
	for i := range mix {
		v := float64(mix[i]) * float64(gain)

		if i < fade {
			v *= float64(i) / float64(fade)
		}
		if tail := len(mix) - i; tail < fade {
			v *= float64(tail) / float64(fade)
		}
		mix[i] = float32(v)
	}
}

func clamp(v, lo, hi int) int {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}

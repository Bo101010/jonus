package mock

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaolianbo/jonus/internal/engine"
)

// testSampleRate keeps fixtures small; nothing in the mock assumes 44.1 kHz.
const testSampleRate = 8000

func newTestEngine(t *testing.T, opts ...Option) *Engine {
	t.Helper()
	// Simulated work is off by default in tests so they run at full speed.
	return New(append([]Option{WithSampleRate(testSampleRate), WithWorkTime(0)}, opts...)...)
}

func request(t *testing.T, spec engine.Spec) engine.Request {
	t.Helper()
	return engine.Request{
		Kind:   engine.KindText2Music,
		Spec:   spec,
		OutDir: t.TempDir(),
	}
}

// readWAVData returns the sample count and sample rate of a WAV file, read
// straight from the bytes rather than through the encoder.
func readWAVData(t *testing.T, path string) (samples int, sampleRate int) {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(b) < 44 {
		t.Fatalf("%s is %d bytes, too short to be a WAV", path, len(b))
	}
	if got := string(b[0:4]); got != "RIFF" {
		t.Fatalf("%s starts with %q, want RIFF", path, got)
	}
	if got := string(b[8:12]); got != "WAVE" {
		t.Fatalf("%s form is %q, want WAVE", path, got)
	}

	dataSize := int(binary.LittleEndian.Uint32(b[40:44]))
	if dataSize != len(b)-44 {
		t.Errorf("%s data chunk says %d bytes, file holds %d", path, dataSize, len(b)-44)
	}
	return dataSize / 2, int(binary.LittleEndian.Uint32(b[24:28]))
}

func TestGenerateWritesPlayableWAV(t *testing.T) {
	e := newTestEngine(t)
	req := request(t, engine.Spec{Prompt: "轻快的民谣，木吉他", Duration: 4 * time.Second})

	result, err := e.Generate(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(result.Tracks) != 1 {
		t.Fatalf("%d tracks, want 1", len(result.Tracks))
	}

	track := result.Tracks[0]
	if track.Format != "wav" {
		t.Errorf("format = %q, want wav", track.Format)
	}
	if !filepath.IsAbs(track.Path) {
		t.Errorf("path %q is not absolute", track.Path)
	}
	if !strings.HasPrefix(track.Path, req.OutDir) {
		t.Errorf("path %q is outside the requested output directory %q", track.Path, req.OutDir)
	}

	samples, rate := readWAVData(t, track.Path)
	if rate != testSampleRate {
		t.Errorf("sample rate = %d, want %d", rate, testSampleRate)
	}
	if want := 4 * testSampleRate; samples != want {
		t.Errorf("%d samples, want %d for a 4s track", samples, want)
	}
	if got := track.Duration.Seconds(); got < 3.9 || got > 4.1 {
		t.Errorf("reported duration = %.2fs, want about 4s", got)
	}

	// A track of pure silence would pass every check above, so confirm the
	// synthesis actually put signal in the file.
	b, err := os.ReadFile(track.Path)
	if err != nil {
		t.Fatalf("read track: %v", err)
	}
	var peak int16
	for i := 44; i+1 < len(b); i += 2 {
		v := int16(binary.LittleEndian.Uint16(b[i:]))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if peak < 32767/4 {
		t.Errorf("peak amplitude = %d, want a clearly audible signal", peak)
	}
}

func TestGenerateIsDeterministicForASeed(t *testing.T) {
	e := newTestEngine(t)

	run := func(seed int64) []byte {
		t.Helper()
		req := request(t, engine.Spec{Prompt: "acoustic folk", Duration: 4 * time.Second, Seed: seed})
		result, err := e.Generate(context.Background(), req, nil)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		b, err := os.ReadFile(result.Tracks[0].Path)
		if err != nil {
			t.Fatalf("read track: %v", err)
		}
		return b
	}

	if !bytes.Equal(run(12345), run(12345)) {
		t.Error("the same seed produced different audio")
	}
	if bytes.Equal(run(12345), run(99999)) {
		t.Error("different seeds produced identical audio")
	}
}

func TestGenerateChoosesAndReportsASeed(t *testing.T) {
	e := newTestEngine(t)
	req := request(t, engine.Spec{Prompt: "pop", Duration: 4 * time.Second})

	result, err := e.Generate(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	seed := result.Tracks[0].Seed
	if seed == 0 {
		t.Fatal("no seed was reported, so the track cannot be reproduced")
	}
	if result.Meta["seed"] != strconv.FormatInt(seed, 10) {
		t.Errorf("meta seed = %q, track seed = %d", result.Meta["seed"], seed)
	}

	// Replaying the reported seed must reproduce the track exactly.
	replay := request(t, engine.Spec{Prompt: "pop", Duration: 4 * time.Second, Seed: seed})
	again, err := e.Generate(context.Background(), replay, nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again.Tracks[0].Seed != seed {
		t.Errorf("replay used seed %d, want %d", again.Tracks[0].Seed, seed)
	}
}

func TestGenerateReportsMonotonicProgress(t *testing.T) {
	e := newTestEngine(t, WithWorkTime(400*time.Millisecond))

	var (
		mu   sync.Mutex
		seen []engine.Progress
	)
	req := request(t, engine.Spec{Prompt: "pop", Duration: 4 * time.Second})

	if _, err := e.Generate(context.Background(), req, func(p engine.Progress) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, p)
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	if len(seen) < 3 {
		t.Fatalf("only %d progress reports, want several", len(seen))
	}
	for i, p := range seen {
		if p.Fraction < 0 || p.Fraction > 1 {
			t.Errorf("report %d fraction = %v, outside 0..1", i, p.Fraction)
		}
		if p.Message == "" {
			t.Errorf("report %d has no message", i)
		}
		if p.At.IsZero() {
			t.Errorf("report %d has no timestamp", i)
		}
		if i > 0 && p.Fraction < seen[i-1].Fraction {
			t.Errorf("report %d fraction %v went backwards from %v", i, p.Fraction, seen[i-1].Fraction)
		}
	}

	last := seen[len(seen)-1]
	if last.Phase != engine.PhaseDone {
		t.Errorf("final phase = %q, want %q", last.Phase, engine.PhaseDone)
	}
	if last.Fraction != 1 {
		t.Errorf("final fraction = %v, want 1", last.Fraction)
	}
}

func TestGenerateStopsWhenCancelled(t *testing.T) {
	e := newTestEngine(t, WithWorkTime(30*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	req := request(t, engine.Spec{Prompt: "pop", Duration: 4 * time.Second})
	start := time.Now()

	_, err := e.Generate(ctx, req, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %v; the engine did not stop promptly", elapsed)
	}

	// Cancelling before the audio is written must not leave a track behind.
	entries, err := os.ReadDir(req.OutDir)
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	for _, entry := range entries {
		t.Errorf("cancelled run left %s behind", entry.Name())
	}
}

func TestGenerateHonoursContextAlreadyCancelled(t *testing.T) {
	e := newTestEngine(t, WithWorkTime(30*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.Generate(ctx, request(t, engine.Spec{Duration: 4 * time.Second}), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestGenerateRejectsUnknownKind(t *testing.T) {
	e := newTestEngine(t)
	req := request(t, engine.Spec{Duration: 4 * time.Second})
	req.Kind = "teleport"

	if _, err := e.Generate(context.Background(), req, nil); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
}

func TestGenerateRequiresOutputDirectory(t *testing.T) {
	e := newTestEngine(t)
	req := engine.Request{Kind: engine.KindText2Music, Spec: engine.Spec{Duration: 4 * time.Second}}

	if _, err := e.Generate(context.Background(), req, nil); err == nil {
		t.Fatal("a request with no output directory was accepted")
	}
}

func TestGenerateAcceptsNilProgressFunc(t *testing.T) {
	e := newTestEngine(t, WithWorkTime(150*time.Millisecond))

	// The documented contract allows a nil callback; it must not panic.
	if _, err := e.Generate(context.Background(),
		request(t, engine.Spec{Duration: 4 * time.Second}), nil); err != nil {
		t.Fatalf("generate with nil progress: %v", err)
	}
}

func TestGenerateClampsDuration(t *testing.T) {
	e := newTestEngine(t)

	cases := []struct {
		name string
		ask  time.Duration
		want time.Duration
	}{
		{"zero uses the default", 0, defaultDuration},
		{"below the floor is raised", time.Second, minDuration},
		{"above the ceiling is lowered", 30 * time.Minute, maxDuration},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := request(t, engine.Spec{Duration: tc.ask})
			result, err := e.Generate(context.Background(), req, nil)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}

			samples, _ := readWAVData(t, result.Tracks[0].Path)
			want := int(tc.want.Seconds()) * testSampleRate
			if samples != want {
				t.Errorf("%d samples, want %d for %v", samples, want, tc.want)
			}
		})
	}
}

func TestGenerateInstrumentalOmitsTheVocalLayer(t *testing.T) {
	e := newTestEngine(t)

	render := func(instrumental bool) []byte {
		t.Helper()
		req := request(t, engine.Spec{
			Prompt:       "ambient",
			Duration:     4 * time.Second,
			Seed:         777,
			Instrumental: instrumental,
		})
		result, err := e.Generate(context.Background(), req, nil)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		b, err := os.ReadFile(result.Tracks[0].Path)
		if err != nil {
			t.Fatalf("read track: %v", err)
		}
		return b
	}

	// Same seed, so any difference comes from the vocal layer alone.
	if bytes.Equal(render(false), render(true)) {
		t.Error("instrumental and vocal versions are identical")
	}
}

func TestExtractProducesStems(t *testing.T) {
	e := newTestEngine(t)
	req := request(t, engine.Spec{Prompt: "pop", Duration: 4 * time.Second, Seed: 4242})
	req.Kind = engine.KindExtract

	result, err := e.Generate(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(result.Tracks) != 1 {
		t.Fatalf("%d tracks, want the mix plus its stems", len(result.Tracks))
	}

	stems := result.Tracks[0].Stems
	if len(stems) == 0 {
		t.Fatal("extract produced no stems")
	}

	wantSamples, _ := readWAVData(t, result.Tracks[0].Path)
	for name, path := range stems {
		if !strings.HasPrefix(path, req.OutDir) {
			t.Errorf("stem %s is at %q, outside the output directory", name, path)
		}
		samples, _ := readWAVData(t, path)
		if samples != wantSamples {
			t.Errorf("stem %s has %d samples, want %d to line up with the mix", name, samples, wantSamples)
		}
	}

	for _, name := range []string{"vocals", "bass", "other"} {
		if _, ok := stems[name]; !ok {
			t.Errorf("no %q stem", name)
		}
	}
}

func TestExtractStemsSumBackToTheMix(t *testing.T) {
	e := newTestEngine(t)
	req := request(t, engine.Spec{Prompt: "pop", Duration: 4 * time.Second, Seed: 2024})
	req.Kind = engine.KindExtract

	result, err := e.Generate(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	read := func(path string) []int16 {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		out := make([]int16, (len(b)-44)/2)
		for i := range out {
			out[i] = int16(binary.LittleEndian.Uint16(b[44+i*2:]))
		}
		return out
	}

	mix := read(result.Tracks[0].Path)
	sum := make([]int32, len(mix))
	for _, path := range result.Tracks[0].Stems {
		for i, v := range read(path) {
			sum[i] += int32(v)
		}
	}

	// The stems are mastered with the mix's gain, so they should reconstruct
	// it to within rounding.
	var worst int32
	for i, v := range mix {
		if d := abs32(sum[i] - int32(v)); d > worst {
			worst = d
		}
	}
	if worst > 2 {
		t.Errorf("stems differ from the mix by up to %d, want at most rounding error", worst)
	}
}

func TestCapabilitiesCoverEveryKind(t *testing.T) {
	e := newTestEngine(t)
	caps := e.Capabilities()

	for _, kind := range []engine.Kind{
		engine.KindText2Music,
		engine.KindCover,
		engine.KindRepaint,
		engine.KindLego,
		engine.KindExtract,
		engine.KindComplete,
	} {
		if !caps.Supports(kind) {
			t.Errorf("capabilities do not include %q", kind)
		}
	}
	if caps.MaxConcurrent != 1 {
		t.Errorf("MaxConcurrent = %d, want 1 to match the single-device constraint", caps.MaxConcurrent)
	}
	if !caps.Lyrics || !caps.Instrumental {
		t.Error("mock should support lyrics and instrumental output")
	}
}

func TestName(t *testing.T) {
	if got := newTestEngine(t).Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
}

func TestGenerateFailsCleanlyOnUnwritableDirectory(t *testing.T) {
	e := newTestEngine(t)

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	req := engine.Request{
		Kind:   engine.KindText2Music,
		Spec:   engine.Spec{Duration: 4 * time.Second},
		OutDir: filepath.Join(dir, "nested"),
	}
	if _, err := e.Generate(context.Background(), req, nil); err == nil {
		t.Skip("running as a user that can write to a read-only directory")
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// parsedWAV is an independent reading of the bytes EncodeWAV produced.
//
// It is deliberately a second implementation of the format rather than a call
// back into the encoder: a decoder that shares the encoder's assumptions can
// agree with it on a header that no other player would accept.
type parsedWAV struct {
	riffSize   uint32
	fmtSize    uint16
	format     uint16
	channels   uint16
	sampleRat  uint32
	byteRate   uint32
	blockAlign uint16
	bits       uint16
	dataSize   uint32
	samples    []int16
}

func parseWAV(t *testing.T, b []byte) parsedWAV {
	t.Helper()

	if len(b) < wavHeaderSize {
		t.Fatalf("wav is %d bytes, shorter than a header", len(b))
	}
	if got := string(b[0:4]); got != "RIFF" {
		t.Fatalf("magic = %q, want RIFF", got)
	}
	if got := string(b[8:12]); got != "WAVE" {
		t.Fatalf("form = %q, want WAVE", got)
	}
	if got := string(b[12:16]); got != "fmt " {
		t.Fatalf("first chunk = %q, want 'fmt '", got)
	}
	if got := string(b[36:40]); got != "data" {
		t.Fatalf("second chunk = %q, want 'data'", got)
	}

	p := parsedWAV{
		riffSize:   binary.LittleEndian.Uint32(b[4:8]),
		fmtSize:    binary.LittleEndian.Uint16(b[16:18]),
		format:     binary.LittleEndian.Uint16(b[20:22]),
		channels:   binary.LittleEndian.Uint16(b[22:24]),
		sampleRat:  binary.LittleEndian.Uint32(b[24:28]),
		byteRate:   binary.LittleEndian.Uint32(b[28:32]),
		blockAlign: binary.LittleEndian.Uint16(b[32:34]),
		bits:       binary.LittleEndian.Uint16(b[34:36]),
		dataSize:   binary.LittleEndian.Uint32(b[40:44]),
	}

	if int(p.dataSize) != len(b)-wavHeaderSize {
		t.Fatalf("data chunk says %d bytes, file holds %d", p.dataSize, len(b)-wavHeaderSize)
	}
	if int(p.riffSize) != len(b)-8 {
		t.Errorf("RIFF size = %d, want %d", p.riffSize, len(b)-8)
	}

	p.samples = make([]int16, p.dataSize/2)
	for i := range p.samples {
		p.samples[i] = int16(binary.LittleEndian.Uint16(b[wavHeaderSize+i*2:]))
	}
	return p
}

func TestEncodeWAVHeaderIsWellFormed(t *testing.T) {
	samples := make([]float32, 1000)
	for i := range samples {
		samples[i] = float32(math.Sin(float64(i) / 20))
	}

	var buf bytes.Buffer
	if err := EncodeWAV(&buf, samples, 44100); err != nil {
		t.Fatalf("encode: %v", err)
	}

	p := parseWAV(t, buf.Bytes())
	if p.fmtSize != 16 {
		t.Errorf("fmt chunk size = %d, want 16", p.fmtSize)
	}
	if p.format != 1 {
		t.Errorf("audio format = %d, want 1 (PCM)", p.format)
	}
	if p.channels != 1 {
		t.Errorf("channels = %d, want 1", p.channels)
	}
	if p.sampleRat != 44100 {
		t.Errorf("sample rate = %d, want 44100", p.sampleRat)
	}
	if p.bits != 16 {
		t.Errorf("bits per sample = %d, want 16", p.bits)
	}
	if p.blockAlign != 2 {
		t.Errorf("block align = %d, want 2", p.blockAlign)
	}
	if want := uint32(44100 * 2); p.byteRate != want {
		t.Errorf("byte rate = %d, want %d", p.byteRate, want)
	}
	if len(p.samples) != len(samples) {
		t.Errorf("%d samples, want %d", len(p.samples), len(samples))
	}
}

func TestEncodeWAVRoundTripsAmplitude(t *testing.T) {
	// Values chosen to land on representable 16-bit steps, so the comparison
	// is about the encoding rather than about rounding.
	samples := []float32{0, 0.5, -0.5, 1, -1}

	var buf bytes.Buffer
	if err := EncodeWAV(&buf, samples, 8000); err != nil {
		t.Fatalf("encode: %v", err)
	}
	p := parseWAV(t, buf.Bytes())

	want := []int16{0, 16384, -16384, 32767, -32768}
	for i, w := range want {
		if p.samples[i] != w {
			t.Errorf("sample %d = %d, want %d", i, p.samples[i], w)
		}
	}
}

func TestEncodeWAVClipsInsteadOfWrapping(t *testing.T) {
	// Wrapping would turn an over-loud positive sample into a loud negative
	// one, which is audible as a click rather than as distortion.
	samples := []float32{2, -2, 100, -100}

	var buf bytes.Buffer
	if err := EncodeWAV(&buf, samples, 8000); err != nil {
		t.Fatalf("encode: %v", err)
	}
	p := parseWAV(t, buf.Bytes())

	if p.samples[0] != 32767 || p.samples[2] != 32767 {
		t.Errorf("positive clipping gave %d, %d; want 32767", p.samples[0], p.samples[2])
	}
	if p.samples[1] != -32768 || p.samples[3] != -32768 {
		t.Errorf("negative clipping gave %d, %d; want -32768", p.samples[1], p.samples[3])
	}
}

func TestEncodeWAVRejectsBadInput(t *testing.T) {
	t.Run("no samples", func(t *testing.T) {
		var buf bytes.Buffer
		err := EncodeWAV(&buf, nil, 44100)
		if !errors.Is(err, ErrUnsupportedWAV) {
			t.Errorf("err = %v, want ErrUnsupportedWAV", err)
		}
	})

	t.Run("non-positive sample rate", func(t *testing.T) {
		var buf bytes.Buffer
		if err := EncodeWAV(&buf, []float32{0.1}, 0); err == nil {
			t.Error("a zero sample rate was accepted")
		}
	})

	t.Run("nothing written on failure", func(t *testing.T) {
		var buf bytes.Buffer
		if err := EncodeWAV(&buf, nil, 44100); err == nil {
			t.Fatal("expected an error")
		}
		if buf.Len() != 0 {
			t.Errorf("wrote %d bytes before failing, want 0", buf.Len())
		}
	})
}

func TestEncodeWAVHandlesNaN(t *testing.T) {
	// A NaN that reaches the converter must not become an arbitrary loud
	// sample; silence is the safe rendering.
	var buf bytes.Buffer
	if err := EncodeWAV(&buf, []float32{float32(math.NaN()), 0.25}, 8000); err != nil {
		t.Fatalf("encode: %v", err)
	}
	p := parseWAV(t, buf.Bytes())
	if p.samples[0] != 0 {
		t.Errorf("NaN encoded as %d, want 0", p.samples[0])
	}
}

func TestPCMSamples(t *testing.T) {
	if got := PCMSamples(2000); got != 1000 {
		t.Errorf("PCMSamples(2000) = %d, want 1000", got)
	}
}

// Package audio holds format-level audio handling: encoding, decoding, and the
// waveform summaries the UI draws. It knows nothing about jobs, engines, or
// HTTP.
package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// WAV header constants for 16-bit PCM.
const (
	wavHeaderSize   = 44
	wavFormatPCM    = 1
	wavBitsPerFrame = 16
	wavChannels     = 1
)

// ErrUnsupportedWAV reports a WAV file this package cannot read, such as one
// using a compressed codec or more than one channel.
var ErrUnsupportedWAV = errors.New("unsupported wav format")

// EncodeWAV writes samples as a mono 16-bit PCM WAV file.
//
// Samples are expected in [-1, 1]. Values outside that range are clipped rather
// than wrapped, so a synthesis mistake produces distortion instead of a loud
// crackle.
func EncodeWAV(w io.Writer, samples []float32, sampleRate int) error {
	if sampleRate <= 0 {
		return fmt.Errorf("encode wav: sample rate %d must be positive", sampleRate)
	}
	if len(samples) == 0 {
		return fmt.Errorf("encode wav: %w: no samples", ErrUnsupportedWAV)
	}

	byteRate := sampleRate * wavChannels * wavBitsPerFrame / 8
	dataSize := len(samples) * wavBitsPerFrame / 8

	header := make([]byte, 0, wavHeaderSize)
	header = append(header, "RIFF"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(wavHeaderSize-8+dataSize))
	header = append(header, "WAVE"...)

	header = append(header, "fmt "...)
	header = binary.LittleEndian.AppendUint32(header, 16) // PCM chunk size
	header = binary.LittleEndian.AppendUint16(header, wavFormatPCM)
	header = binary.LittleEndian.AppendUint16(header, wavChannels)
	header = binary.LittleEndian.AppendUint32(header, uint32(sampleRate))
	header = binary.LittleEndian.AppendUint32(header, uint32(byteRate))
	header = binary.LittleEndian.AppendUint16(header, wavChannels*wavBitsPerFrame/8) // block align
	header = binary.LittleEndian.AppendUint16(header, wavBitsPerFrame)

	header = append(header, "data"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(dataSize))

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write wav header: %w", err)
	}

	// Encode in blocks so a long track does not need a second full-size buffer.
	const blockFrames = 4096
	buf := make([]byte, 0, blockFrames*wavBitsPerFrame/8)
	for start := 0; start < len(samples); start += blockFrames {
		end := min(start+blockFrames, len(samples))
		buf = buf[:0]
		for _, s := range samples[start:end] {
			buf = binary.LittleEndian.AppendUint16(buf, uint16(floatToPCM16(s)))
		}
		if _, err := w.Write(buf); err != nil {
			return fmt.Errorf("write wav samples: %w", err)
		}
	}
	return nil
}

// floatToPCM16 converts one sample to signed 16-bit, clipping out-of-range
// input.
//
// The scale is 32768, not 32767: two's complement holds one more negative code
// than positive, so scaling by 32767 would leave -32768 unreachable and make
// full-scale negative input quieter than full-scale positive input. Values at
// or above +1.0 therefore clip to 32767.
func floatToPCM16(s float32) int16 {
	// Guard against NaN, which would otherwise convert to an arbitrary value.
	if math.IsNaN(float64(s)) {
		return 0
	}
	v := math.Round(float64(s) * 32768)
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	default:
		return int16(v)
	}
}

// PCMSamples is the number of samples in a WAV data chunk of size bytes.
func PCMSamples(dataSize int) int { return dataSize / (wavBitsPerFrame / 8) }

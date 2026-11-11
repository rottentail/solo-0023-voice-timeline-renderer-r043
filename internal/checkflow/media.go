package checkflow

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math"

	"voice-timeline-renderer/internal/audiocodec"
)

func encodeBase64(payload []byte) string {
	return base64.StdEncoding.EncodeToString(payload)
}

func digestHex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func sineWAV(sampleRate, channels, durationMS int, frequency, amplitude float64) ([]byte, error) {
	frames := sampleRate * durationMS / 1000
	samples := make([]float32, frames*channels)
	for frame := 0; frame < frames; frame++ {
		seconds := float64(frame) / float64(sampleRate)
		first := float32(math.Sin(2*math.Pi*frequency*seconds) * amplitude)
		if channels == 1 {
			samples[frame] = first
			continue
		}
		second := float32(math.Sin(2*math.Pi*frequency*seconds+0.35) * amplitude * 0.8)
		samples[frame*2] = first
		samples[frame*2+1] = second
	}
	return audiocodec.EncodePCM16(sampleRate, channels, samples)
}

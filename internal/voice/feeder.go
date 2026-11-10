package voice

import (
	"context"
	"time"
)

const chunkInterval = 100 * time.Millisecond

func silenceChunk() []byte {
	return make([]byte, chunkBytes)
}

// pace writes one 19200-byte chunk per 100ms. A late tick resyncs to now
// instead of bursting the missed chunks.
func pace(ctx context.Context, clock sleeper, write func([]byte) error, next func() ([]byte, error)) error {
	at := clock.Now()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, err := next()
		if err != nil {
			return err
		}
		if d := at.Sub(clock.Now()); d > 0 {
			if err := clock.Sleep(ctx, d); err != nil {
				return err
			}
		} else if d < 0 {
			at = clock.Now()
		}
		if err := write(chunk); err != nil {
			return err
		}
		at = at.Add(chunkInterval)
	}
}

func packetize(pcm []byte) [][]byte {
	if len(pcm) == 0 {
		return nil
	}
	out := make([][]byte, 0, (len(pcm)+chunkBytes-1)/chunkBytes)
	for i := 0; i < len(pcm); i += chunkBytes {
		buf := make([]byte, chunkBytes)
		end := i + chunkBytes
		if end > len(pcm) {
			copy(buf, pcm[i:])
		} else {
			copy(buf, pcm[i:end])
		}
		out = append(out, buf)
	}
	return out
}

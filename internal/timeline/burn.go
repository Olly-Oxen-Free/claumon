package timeline

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// Buckets per session in a burn series. Enough stops for the heat gradient to
// read as continuous without sending a point per event.
const burnBuckets = 48

// burnCache memoises a session's series against the file it came from.
//
// Building a series means reading the whole transcript, which runs to tens of
// megabytes. A live session's file grows between polls and is recomputed; an
// idle one is read once. Keyed on size as well as path so growth invalidates.
var burnCache sync.Map // path -> burnEntry

type burnEntry struct {
	size   int64
	series []int
}

// BurnSeries returns tokens consumed per time bucket across [start, end].
//
// Only usage-bearing lines matter, and those are a small fraction of a
// transcript, so lines are filtered on a substring before being decoded — the
// difference between reading a 16MB file and parsing it.
func BurnSeries(path string, start, end time.Time) []int {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if v, ok := burnCache.Load(path); ok {
		if e := v.(burnEntry); e.size == info.Size() {
			return e.series
		}
	}

	span := end.Sub(start)
	if span <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	series := make([]int, burnBuckets)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		if !strings.Contains(string(b), `"usage"`) {
			continue
		}
		var l struct {
			Timestamp time.Time `json:"timestamp"`
			Message   *struct {
				Usage *struct {
					InputTokens              int `json:"input_tokens"`
					OutputTokens             int `json:"output_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(b, &l) != nil || l.Message == nil || l.Message.Usage == nil {
			continue
		}
		if l.Timestamp.Before(start) || l.Timestamp.After(end) {
			continue
		}
		u := l.Message.Usage
		total := u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		if total <= 0 {
			continue
		}
		idx := int(float64(l.Timestamp.Sub(start)) / float64(span) * float64(burnBuckets))
		if idx < 0 {
			idx = 0
		}
		if idx >= burnBuckets {
			idx = burnBuckets - 1
		}
		series[idx] += total
	}

	burnCache.Store(path, burnEntry{size: info.Size(), series: series})
	return series
}

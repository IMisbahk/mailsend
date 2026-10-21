package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// spanValue accepts Go durations plus a day unit, so --since 30d and --sent-within 7d work.
type spanValue time.Duration

func (s *spanValue) String() string {
	if s == nil {
		return "0s"
	}
	d := time.Duration(*s)
	if d != 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int64(d/(24*time.Hour)))
	}
	return d.String()
}

func (s *spanValue) Set(raw string) error {
	parsed, err := parseSpan(raw)
	if err != nil {
		return err
	}
	*s = spanValue(parsed)
	return nil
}

func (s *spanValue) Type() string { return "duration" }

func (s spanValue) Duration() time.Duration { return time.Duration(s) }

func parseSpan(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	rest := raw
	for len(rest) > 0 {
		i := 0
		if rest[0] == '-' || rest[0] == '+' {
			i = 1
		}
		start := i
		dot := false
		for i < len(rest) && ((rest[i] >= '0' && rest[i] <= '9') || (rest[i] == '.' && !dot)) {
			if rest[i] == '.' {
				dot = true
			}
			i++
		}
		if i == start {
			return 0, fmt.Errorf("invalid duration %q", raw)
		}
		j := i
		for j < len(rest) && rest[j] >= 'a' && rest[j] <= 'z' {
			j++
		}
		if j == i {
			return 0, fmt.Errorf("invalid duration %q", raw)
		}
		number := rest[:i]
		unit := rest[i:j]
		rest = rest[j:]
		if unit == "d" {
			days, err := strconv.ParseFloat(number, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q", raw)
			}
			total += time.Duration(days * float64(24*time.Hour))
			continue
		}
		part, err := time.ParseDuration(number + unit)
		if err != nil {
			return 0, err
		}
		total += part
	}
	return total, nil
}

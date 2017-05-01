package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

////////////////////////////////////////////////////////////////

func digits10(number int) int {
	count := 1
	for ; number >= 10; number /= 10 {
		count++
	}
	return count
}

func rightpad(text string, total int) string {
	if len(text) >= total {
		return text
	}
	return text + strings.Repeat(" ", total-len(text))
}

func numbered(count int, singular string) string {
	result := strconv.Itoa(count) + " " + singular
	if count != 1 {
		result += "s"
	}
	return result
}

func nth(index int) string {
	if index == 1 {
		return "1st"
	}
	if index == 2 {
		return "2nd"
	}
	if index == 3 {
		return "3rd"
	}
	return fmt.Sprintf("%dth", index)
}

func escape(s string) string {
	s = strings.Replace(s, "_", "\\_", -1)
	s = strings.Replace(s, "*", "\\*", -1)
	s = strings.Replace(s, "`", "\\`", -1)
	return s
}

func bold(s string) string {
	return "**" + s + "**"
}

func italic(s string) string {
	return "*" + s + "*"
}

func bolditalic(s string) string {
	return "***" + s + "***"
}

func mentionUser(UserID string) string {
	return "<@" + UserID + ">"
}

func mentionChannel(ChannelID string) string {
	return "<#" + ChannelID + ">"
}

////////////////////////////////////////////////////////////////

func parseToken(cmd string) (string, string) {
	separators := " \t\n\r"
	splitPoint := strings.IndexAny(cmd, separators)
	if splitPoint == -1 {
		return cmd, ""
	}

	token := cmd[:splitPoint]
	for splitPoint++; splitPoint < len(cmd); splitPoint++ {
		if strings.IndexByte(separators, cmd[splitPoint]) == -1 {
			break
		}
	}

	return token, cmd[splitPoint:]
}

////////////////////////////////////////////////////////////////

// Common longer durations
const (
	Day   = 24 * time.Hour
	Week  = 7 * Day
	Month = 30 * Day
	Year  = 365 * Day
)

func humanize(duration time.Duration) string {
	if duration < 0 {
		duration = -duration
	}

	var (
		relevantDurations = [...]struct {
			time.Duration
			Name string
		}{
			{time.Second, "second"},
			{time.Minute, "minute"},
			{time.Hour, "hour"},
			{Day, "day"},
			{Week, "week"},
			{Month, "month"},
			{12 * Month, "year"}, // for a humanized string, this is better than the exact value; e.g. for 345 days ~= 12 months < 1 year!
		}
	)

	n := sort.Search(len(relevantDurations), func(i int) bool {
		rounded := duration
		if i > 0 {
			// round up to a multiple of the previous unit
			// e.g. when considering hours, round up to the next minute
			// this way, 59 minutes and 33 seconds = 1 hour
			rounded += relevantDurations[i-1].Duration / 2
		}
		return relevantDurations[i].Duration > rounded
	}) - 1

	if n < 0 {
		n = 0
	}

	duration += relevantDurations[n].Duration / 2
	nano := duration.Nanoseconds()
	major := nano / relevantDurations[n].Nanoseconds()

	return numbered(int(major), relevantDurations[n].Name)
}

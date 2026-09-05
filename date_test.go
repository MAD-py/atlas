package atlas

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type DateSuite struct {
	suite.Suite
}

func TestDate(t *testing.T) {
	suite.Run(t, new(DateSuite))
}

func (s *DateSuite) TestNewDate_ProducesMidnightUTCOnTheGivenDay() {
	tests := []struct {
		name  string
		year  int
		month time.Month
		day   int
	}{
		{name: "ordinary day", year: 2026, month: time.March, day: 15},
		{name: "leap day", year: 2024, month: time.February, day: 29},
		{name: "year boundary", year: 1999, month: time.December, day: 31},
		{name: "epoch day", year: 1970, month: time.January, day: 1},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			got := NewDate(tt.year, tt.month, tt.day).Time()
			s.Equal(tt.year, got.Year())
			s.Equal(tt.month, got.Month())
			s.Equal(tt.day, got.Day())
			s.Equal(0, got.Hour())
			s.Equal(0, got.Minute())
			s.Equal(0, got.Second())
			s.Equal(time.UTC, got.Location())
		})
	}
}

func (s *DateSuite) TestString_RendersYYYYMMDD() {
	tests := []struct {
		name string
		date Date
		want string
	}{
		{name: "ordinary day", date: NewDate(2026, time.January, 2), want: "2026-01-02"},
		{name: "single-digit month and day are zero-padded", date: NewDate(2026, time.March, 5), want: "2026-03-05"},
		{name: "double-digit month and day", date: NewDate(2026, time.November, 23), want: "2026-11-23"},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, tt.date.String())
		})
	}
}

func (s *DateSuite) TestMarshalTextUnmarshalText_RoundTrip() {
	tests := []struct {
		name string
		date Date
		want string
	}{
		{name: "ordinary day", date: NewDate(2026, time.September, 5), want: "2026-09-05"},
		{name: "leap day", date: NewDate(2024, time.February, 29), want: "2024-02-29"},
		{name: "pre-epoch", date: NewDate(1965, time.January, 1), want: "1965-01-01"},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			text, err := tt.date.MarshalText()
			s.Require().NoError(err)
			s.Equal(tt.want, string(text))

			var got Date
			s.Require().NoError(got.UnmarshalText(text))
			s.Equal(tt.date, got)
		})
	}
}

func (s *DateSuite) TestUnmarshalText_RejectsMalformedInput() {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty string", input: ""},
		{name: "not a date at all", input: "not-a-date"},
		{name: "full RFC3339 timestamp instead of a date", input: "2026-09-05T00:00:00Z"},
		{name: "wrong separator", input: "2026/09/05"},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var d Date
			s.Error(d.UnmarshalText([]byte(tt.input)))
		})
	}
}

// TestDocument_JSONMarshalsDateAsCalendarDay is the concrete regression test
// for the MarshalText gotcha: without it, encoding/json would reflect into
// Date's underlying time.Time's unexported fields and marshal "{}" instead
// of a date string, or (if it fell back to time.Time's own default json
// behavior some other way) the full RFC3339 timestamp form.
func (s *DateSuite) TestDocument_JSONMarshalsDateAsCalendarDay() {
	doc := Document{Fields: map[string]any{"birthday": NewDate(1990, time.July, 4)}}

	data, err := json.Marshal(doc)
	s.Require().NoError(err)

	var decoded struct {
		Fields map[string]string `json:"Fields"`
	}
	s.Require().NoError(json.Unmarshal(data, &decoded))
	s.Equal("1990-07-04", decoded.Fields["birthday"])
}

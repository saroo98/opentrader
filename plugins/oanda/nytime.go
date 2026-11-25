package main

import "time"

// New York time without a time zone database: US daylight saving runs from the second Sunday of March, 02:00,
// to the first Sunday of November, 02:00.

func nthSunday(year int, month time.Month, n int) int {
	first := int(time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).Weekday()) // Sunday = 0
	return 1 + (7-first)%7 + 7*(n-1)
}

// nyOffset is the number of seconds to add to a UTC time to get New York local time.
func nyOffset(utc int64) int64 {
	year := time.Unix(utc, 0).UTC().Year()
	start := time.Date(year, time.March, nthSunday(year, time.March, 2), 7, 0, 0, 0, time.UTC).Unix()     // 02:00 EST
	end := time.Date(year, time.November, nthSunday(year, time.November, 1), 6, 0, 0, 0, time.UTC).Unix() // 02:00 EDT
	if utc >= start && utc < end {
		return -4 * 3600
	}
	return -5 * 3600
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// tradingDay is the bar time of the forex trading day utc falls in: that day's midnight in New York, in UTC seconds.
// A trading day runs from 17:00 New York the evening before (the forex close).
func tradingDay(utc int64) int64 {
	local := utc + nyOffset(utc)
	day := floorDiv(local+7*3600, 86400) * 86400
	return day - nyOffset(day+5*3600)
}

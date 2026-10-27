package tracking

import (
	"eptaadmin/internal/geoip"
	"eptaadmin/internal/store"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ParseUserAgent is a small heuristic browser/OS/device classifier — there
// is no UA-parsing dependency in this codebase (deliberately: go.mod stays
// dependency-light), so this only needs to be good enough for a "Top
// Browser"/"Top Device" breakdown, not perfectly accurate. Order matters:
// browsers whose UA string also contains another browser's name must be
// checked first (Edge and Opera both contain "Chrome"; Chrome itself
// contains "Safari").
func ParseUserAgent(ua string) (browser, os, device string) {
	switch {
	case strings.Contains(ua, "Edg/") || strings.Contains(ua, "Edge/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/") || strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Safari/") && !strings.Contains(ua, "Chrome/"):
		browser = "Safari"
	default:
		browser = "Other"
	}

	switch {
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad") || strings.Contains(ua, "iPod"):
		os = "iOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		os = "macOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	default:
		os = "Other"
	}

	switch {
	case strings.Contains(ua, "iPad") || strings.Contains(ua, "Tablet"):
		device = "Tablet"
	case strings.Contains(ua, "iPhone") || (strings.Contains(ua, "Android") && strings.Contains(ua, "Mobile")):
		device = "Mobile"
	default:
		device = "Desktop"
	}

	return browser, os, device
}

// SessionSummary is one visitor session, derived from every event row
// sharing a session_id — a record store has no native concept of a
// "session", so this is rebuilt fresh on every dashboard request from the
// raw enter/exit rows (see ComputeDashboardStats).
type SessionSummary struct {
	SessionID   string
	FirstSeen   time.Time
	LastSeen    time.Time
	EntryURL    string
	ExitURL     string
	EventCount  int
	TimeOnPage  float64
	Browser     string
	OS          string
	Device      string
	Country     string // ISO 3166-1 alpha-2, "" if unresolved (see lookupCountry)
	CountryName string // resolved once at session creation, so both the server-rendered Sessions tab and its JSON-driven live refresh show the same full name without the frontend needing its own code→name table
	// Language/Screen/Referrer are captured once, from the first physical
	// row seen for this session (same timing as Browser/OS/Device below) —
	// they're per-visitor/per-landing, not per-event, so there's no "which
	// event's value" ambiguity to resolve. Referrer in particular is the
	// value of document.referrer on the session's very first request,
	// i.e. wherever the visitor actually came from before landing on the
	// tracked site — not the referrer of whichever page they're on now.
	Language string
	Screen   string
	Referrer string
}

// DurationSeconds is how long the session has been observed for, i.e. the
// span between its first and last recorded event. A single-event (bounced)
// session has a duration of 0, not a missing value — it's simply accurate.
func (s SessionSummary) DurationSeconds() float64 {
	return s.LastSeen.Sub(s.FirstSeen).Seconds()
}

type BreakdownEntry struct {
	Name    string
	Count   int
	Percent float64
	Color   string // only set for BrowserBreakdown, used by the Overview donut
}

// CountryBreakdownEntry is one row of the map's "top countries" legend —
// Code is the ISO 3166-1 alpha-2 id, matching a <path id="CODE"> in the
// embedded world map SVG so the frontend can color it directly.
type CountryBreakdownEntry struct {
	Code      string
	Name      string
	Count     int
	Percent   float64
	FillColor string // ready-to-use CSS color, server-rendered so the map has correct colors on first paint (JS only recomputes this on a range switch)
}

// donutPalette assigns a stable color to each browser breakdown slice —
// there's no charting library in this codebase (deliberately: the donut
// is a hand-rolled CSS conic-gradient, consistent with every other
// "chart" here being plain SVG/CSS), so the palette and the gradient
// string are built in Go rather than by a JS chart config.
var donutPalette = []string{"#34d399", "#fb923c", "#60a5fa", "#f472b6", "#c084fc", "#facc15", "#94a3b8"}

// buildDonutGradient turns a percent-sorted breakdown into a ready-to-use
// CSS conic-gradient() argument string (and assigns each entry's Color to
// match, so the legend swatches line up with the slices). Entries beyond
// the palette size share the last color rather than panic/wrap oddly —
// acceptable since a >7-way browser split is already an unusual case.
func buildDonutGradient(entries []BreakdownEntry) string {
	if len(entries) == 0 {
		return "var(--eb-border) 0 100%"
	}
	var b strings.Builder
	cumulative := 0.0
	for i := range entries {
		color := donutPalette[i]
		if i >= len(donutPalette) {
			color = donutPalette[len(donutPalette)-1]
		}
		entries[i].Color = color
		start := cumulative
		cumulative += entries[i].Percent
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(color)
		b.WriteString(" ")
		b.WriteString(formatPercent(start))
		b.WriteString("% ")
		b.WriteString(formatPercent(cumulative))
		b.WriteString("%")
	}
	return b.String()
}

func formatPercent(p float64) string {
	// Two decimals is plenty for a conic-gradient stop and avoids
	// strconv's default shortest-representation noise for values like
	// 33.333333333333336.
	whole := int(p * 100)
	return strconv.FormatFloat(float64(whole)/100, 'f', 2, 64)
}

// countryFillColor computes the world map's per-country fill, scaled by
// visit share relative to the busiest country in the breakdown (accent
// green, opacity 0.15–0.90). Rendered server-side so the map has correct
// colors on first paint — the Overview tab's JS (renderCountryMap in
// templates/tracking_dashboard.html) recomputes the exact same formula
// when the range dropdown changes, since that path stays JS-only.
func countryFillColor(count, maxCount int) string {
	intensity := 0.0
	if maxCount > 0 {
		intensity = float64(count) / float64(maxCount)
	}
	opacity := 0.15 + 0.75*intensity
	return "rgba(52, 211, 153, " + strconv.FormatFloat(opacity, 'f', 2, 64) + ")"
}

// DashboardStats is everything the Overview tab needs, computed in one
// pass over the table's record store — see ComputeDashboardStats.
type DashboardStats struct {
	Range               string
	TotalEvents         int
	UniqueSessions      int
	ActiveSessions      int
	BounceRatePercent   float64
	TopDevice           string
	TopBrowser          string
	BrowserBreakdown    []BreakdownEntry
	DeviceBreakdown     []BreakdownEntry
	LanguageBreakdown   []BreakdownEntry
	ScreenBreakdown     []BreakdownEntry
	ReferrerBreakdown   []BreakdownEntry
	DonutGradient       string
	TopPages            []BreakdownEntry
	CountryBreakdown    []CountryBreakdownEntry
	AvgSessionDuration  float64
	AvgEventsPerSession float64
	Trend               []TrendPoint
	TrendMax            int
	Sessions            []SessionSummary
	RecentEvents        []RecentEvent
	ActivityPage        int
	ActivityTotalPages  int
}

// TrendPoint is one bucket of the Overview tab's events/sessions-over-time
// chart — see buildTrendBuckets.
type TrendPoint struct {
	Label    string
	Events   int
	Sessions int
}

// referrerDirectLabel is the bucket name for events with no referrer at
// all (document.referrer was empty — a bookmark, a typed URL, most native
// apps/QR codes) — the standard "Direct" label any analytics tool uses.
const referrerDirectLabel = "Direct"

// referrerSource reduces a raw referrer URL to its traffic-source label:
// the referring site's bare host (scheme and path dropped — two pages on
// the same referring site are the same traffic source), "Direct" for no
// referrer, or the raw value verbatim if it doesn't parse as a URL.
func referrerSource(raw string) string {
	if raw == "" {
		return referrerDirectLabel
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.")
}

// trendBucketKey/trendBucketLabel split a timestamp into its bucket's
// stable map key (sortless string, so buckets are built by walking the
// [start, end] window rather than by sorting map keys) and its
// display label, at hourly or daily granularity.
func trendBucketKey(t time.Time, hourly bool) string {
	if hourly {
		return t.Format("2006-01-02T15")
	}
	return t.Format("2006-01-02")
}

func trendBucketLabel(t time.Time, hourly bool) string {
	if hourly {
		return t.Format("15:00")
	}
	return t.Format("02/01")
}

// maxTrendDays bounds how far back the "all time" range's trend chart
// reaches — long-running sites otherwise draw one bar per day since
// creation, which both renders terribly and isn't very readable anyway.
// Every other range-scoped number on the page still reflects the full
// history; only this chart's x-axis is clamped.
const maxTrendDays = 90

// buildTrendBuckets walks [start, end] in fixed-size steps (hourly for the
// 24h range, daily otherwise — see ComputeDashboardStats) producing one
// TrendPoint per step, including zero-activity steps, so the chart reads
// as a continuous timeline rather than skipping gaps.
func buildTrendBuckets(start, end time.Time, hourly bool, eventCounts map[string]int, sessionSets map[string]map[string]bool) []TrendPoint {
	step := 24 * time.Hour
	cur := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
	if hourly {
		step = time.Hour
		cur = time.Date(start.Year(), start.Month(), start.Day(), start.Hour(), 0, 0, 0, start.Location())
	}
	var pts []TrendPoint
	for !cur.After(end) {
		key := trendBucketKey(cur, hourly)
		pts = append(pts, TrendPoint{
			Label:    trendBucketLabel(cur, hourly),
			Events:   eventCounts[key],
			Sessions: len(sessionSets[key]),
		})
		cur = cur.Add(step)
	}
	return pts
}

// RecentEvent is the compact shape shown in the Overview tab's "recent
// activity" list — a browser-parsed summary, not the raw row.
type RecentEvent struct {
	EventType string
	URL       string
	SessionID string
	Browser   string
	Timestamp time.Time
}

// ActiveSessionWindow defines "active right now" for the ActiveSessions
// KPI — deliberately independent of the selected date range (a session
// active in the last 5 minutes is "active" whether the dashboard is
// showing the last 24h or the last 30 days).
const ActiveSessionWindow = 5 * time.Minute

// activityPageSize mirrors the constant of the same name in the root
// activity.go (kept as a literal, not imported — see the comment where
// it's used below).
const activityPageSize = 8

// ParseRangeParam turns the Overview tab's range dropdown value into a
// cutoff time. Unrecognized/empty values default to 24h rather than
// erroring — a public-ish dashboard query param isn't worth failing hard
// on.
func ParseRangeParam(v string) (cutoff time.Time, label string) {
	now := time.Now()
	switch v {
	case "7d":
		return now.Add(-7 * 24 * time.Hour), "7d"
	case "30d":
		return now.Add(-30 * 24 * time.Hour), "30d"
	case "all":
		return time.Time{}, "all"
	default:
		return now.Add(-24 * time.Hour), "24h"
	}
}

// ComputeDashboardStats aggregates a tracked site's raw event rows into
// everything the analytics dashboard displays. It always loads the whole
// record store into memory (see store.RecordStore/store.LoadRecordStore in
// jsondata.go) — there is no pagination or indexing anywhere in this
// codebase, and handleTableGrid already does the same full-load on every
// request, so this is consistent with existing practice rather than a new
// scalability regression.
func ComputeDashboardStats(records store.RecordStore, cutoff time.Time, rangeLabel string, activityPage int) DashboardStats {
	now := time.Now()
	sessions := map[string]*SessionSummary{}
	order := []string{}

	totalInRange := 0
	pageCounts := map[string]int{}
	var recent []RecentEvent

	// hourly/trendEventCounts/trendSessionSets/minInRangeTs feed the
	// Overview tab's events-over-time chart (buildTrendBuckets, called
	// after this loop once the bucketing window is known) — hourly
	// buckets for the 24h range (a single day's shape is worth seeing),
	// daily otherwise.
	hourly := rangeLabel == "24h"
	trendEventCounts := map[string]int{}
	trendSessionSets := map[string]map[string]bool{}
	var minInRangeTs time.Time

	for _, rec := range records {
		sid, _ := rec["session_id"].(string)
		if sid == "" {
			continue
		}
		ts, ok := parseEventTimestamp(rec["timestamp"])
		if !ok {
			continue
		}

		s, exists := sessions[sid]
		if !exists {
			ua, _ := rec["user_agent"].(string)
			browser, os, device := ParseUserAgent(ua)
			country, countryName := "", ""
			if ipStr, _ := rec["ip_public"].(string); ipStr != "" {
				if code, ok := geoip.LookupCountry(net.ParseIP(ipStr)); ok {
					country = code
					countryName = geoip.CountryName(code)
				}
			}
			language, _ := rec["language"].(string)
			screen, _ := rec["screen"].(string)
			referrer, _ := rec["referrer"].(string)
			s = &SessionSummary{
				SessionID: sid, FirstSeen: ts, LastSeen: ts,
				Browser: browser, OS: os, Device: device, Country: country, CountryName: countryName,
				Language: language, Screen: screen, Referrer: referrer,
			}
			sessions[sid] = s
			order = append(order, sid)
		}
		if ts.Before(s.FirstSeen) {
			s.FirstSeen = ts
		}
		if ts.After(s.LastSeen) {
			s.LastSeen = ts
		}

		eventType, _ := rec["event_type"].(string)
		url, _ := rec["url"].(string)
		inRange := cutoff.IsZero() || !ts.Before(cutoff)

		// Event count / entry-exit URLs / bounce detection only count events
		// that fall inside the selected range — a session that straddles the
		// range boundary is still summarized using only its in-range events,
		// consistent with every other range-scoped number on the page.
		if inRange {
			s.EventCount++
			totalInRange++
			if url != "" {
				pageCounts[url]++
			}
			switch eventType {
			case "enter":
				if s.EntryURL == "" {
					s.EntryURL = url
				}
			case "exit":
				s.ExitURL = url
				if top, ok := rec["time_on_page"].(float64); ok {
					s.TimeOnPage += top
				}
			}
			recent = append(recent, RecentEvent{EventType: eventType, URL: url, SessionID: sid, Browser: s.Browser, Timestamp: ts})

			if minInRangeTs.IsZero() || ts.Before(minInRangeTs) {
				minInRangeTs = ts
			}
			key := trendBucketKey(ts, hourly)
			trendEventCounts[key]++
			if trendSessionSets[key] == nil {
				trendSessionSets[key] = map[string]bool{}
			}
			trendSessionSets[key][sid] = true
		}
	}

	// Newest first, paginated the same way as the global Activité page
	// (activityPageSize, root activity.go) — the Real-time Stream tab
	// remains the full, live-polling view; this one is a browsable log
	// instead. Duplicated as a literal rather than imported: activity.go
	// stays in package main (it's an App-coupled orchestration file, not
	// a candidate for its own package — see the reorg's scope notes), so
	// pulling in a single page-size constant from there isn't worth a
	// package dependency in either direction.
	allRecentDesc := make([]RecentEvent, len(recent))
	for i, e := range recent {
		allRecentDesc[len(recent)-1-i] = e
	}
	activityTotalPages := (len(allRecentDesc) + activityPageSize - 1) / activityPageSize
	if activityTotalPages == 0 {
		activityTotalPages = 1
	}
	if activityPage < 1 {
		activityPage = 1
	}
	if activityPage > activityTotalPages {
		activityPage = activityTotalPages
	}
	pageStart := (activityPage - 1) * activityPageSize
	pageEnd := pageStart + activityPageSize
	if pageStart > len(allRecentDesc) {
		pageStart = len(allRecentDesc)
	}
	if pageEnd > len(allRecentDesc) {
		pageEnd = len(allRecentDesc)
	}
	recentEvents := allRecentDesc[pageStart:pageEnd]

	activeSessions := 0
	for _, s := range sessions {
		if now.Sub(s.LastSeen) <= ActiveSessionWindow {
			activeSessions++
		}
	}

	// Sessions with zero in-range events (only relevant when cutoff > their
	// only events) are dropped from every range-scoped figure below — they
	// simply didn't happen during the selected window.
	var inRangeSessions []*SessionSummary
	for _, sid := range order {
		if sessions[sid].EventCount > 0 {
			inRangeSessions = append(inRangeSessions, sessions[sid])
		}
	}

	bounced := 0
	browserCounts := map[string]int{}
	for _, s := range inRangeSessions {
		if s.EventCount == 1 {
			bounced++
		}
		browserCounts[s.Browser]++
	}

	uniqueSessions := len(inRangeSessions)
	bounceRate := 0.0
	if uniqueSessions > 0 {
		bounceRate = float64(bounced) / float64(uniqueSessions) * 100
	}

	browserBreakdown := breakdownFromCounts(browserCounts, uniqueSessions)
	donutGradient := buildDonutGradient(browserBreakdown)
	topBrowser := "—"
	if len(browserBreakdown) > 0 {
		topBrowser = browserBreakdown[0].Name
	}

	deviceCounts := map[string]int{}
	languageCounts := map[string]int{}
	screenCounts := map[string]int{}
	referrerCounts := map[string]int{}
	totalDuration := 0.0
	for _, s := range inRangeSessions {
		deviceCounts[s.Device]++
		if s.Language != "" {
			languageCounts[s.Language]++
		}
		if s.Screen != "" {
			screenCounts[s.Screen]++
		}
		referrerCounts[referrerSource(s.Referrer)]++
		totalDuration += s.DurationSeconds()
	}
	deviceBreakdown := breakdownFromCounts(deviceCounts, uniqueSessions)
	topDevice := "—"
	if len(deviceBreakdown) > 0 {
		topDevice = deviceBreakdown[0].Name
	}
	languageBreakdown := breakdownFromCounts(languageCounts, uniqueSessions)
	if len(languageBreakdown) > 8 {
		languageBreakdown = languageBreakdown[:8]
	}
	screenBreakdown := breakdownFromCounts(screenCounts, uniqueSessions)
	if len(screenBreakdown) > 8 {
		screenBreakdown = screenBreakdown[:8]
	}
	referrerBreakdown := breakdownFromCounts(referrerCounts, uniqueSessions)
	if len(referrerBreakdown) > 8 {
		referrerBreakdown = referrerBreakdown[:8]
	}

	avgSessionDuration := 0.0
	avgEventsPerSession := 0.0
	if uniqueSessions > 0 {
		avgSessionDuration = totalDuration / float64(uniqueSessions)
		avgEventsPerSession = float64(totalInRange) / float64(uniqueSessions)
	}

	topPages := breakdownFromCounts(pageCounts, totalInRange)
	if len(topPages) > 8 {
		topPages = topPages[:8]
	}

	// Trend window: the range's own cutoff for 24h/7d/30d, but "all" has no
	// cutoff (time.Time{}) by definition, so it's clamped to maxTrendDays —
	// or to the earliest in-range event if the site's history is shorter
	// than that, so a brand-new site doesn't draw 90 days of empty bars.
	trendStart := cutoff
	if rangeLabel == "all" {
		trendStart = now.Add(-maxTrendDays * 24 * time.Hour)
		if !minInRangeTs.IsZero() && minInRangeTs.After(trendStart) {
			trendStart = minInRangeTs
		}
	}
	var trend []TrendPoint
	if !trendStart.IsZero() {
		trend = buildTrendBuckets(trendStart, now, hourly, trendEventCounts, trendSessionSets)
	}
	trendMax := 1
	for _, p := range trend {
		if p.Events > trendMax {
			trendMax = p.Events
		}
		if p.Sessions > trendMax {
			trendMax = p.Sessions
		}
	}

	countryCounts := map[string]int{}
	for _, s := range inRangeSessions {
		if s.Country == "" {
			continue // unresolved (IPv6, private/loopback, or unknown range) — excluded from the map/legend rather than shown as a fake "unknown" slice
		}
		countryCounts[s.Country]++
	}
	countryBreakdownRaw := breakdownFromCounts(countryCounts, uniqueSessions)
	maxCountryCount := 0
	if len(countryBreakdownRaw) > 0 {
		maxCountryCount = countryBreakdownRaw[0].Count
	}
	countryBreakdown := make([]CountryBreakdownEntry, len(countryBreakdownRaw))
	for i, e := range countryBreakdownRaw {
		countryBreakdown[i] = CountryBreakdownEntry{
			Code: e.Name, Name: geoip.CountryName(e.Name), Count: e.Count, Percent: e.Percent,
			FillColor: countryFillColor(e.Count, maxCountryCount),
		}
	}
	if len(countryBreakdown) > 10 {
		countryBreakdown = countryBreakdown[:10]
	}

	summaries := make([]SessionSummary, 0, len(inRangeSessions))
	for _, s := range inRangeSessions {
		summaries = append(summaries, *s)
	}
	sortSessionsByLastSeenDesc(summaries)

	return DashboardStats{
		Range:               rangeLabel,
		TotalEvents:         totalInRange,
		UniqueSessions:      uniqueSessions,
		ActiveSessions:      activeSessions,
		BounceRatePercent:   bounceRate,
		TopDevice:           topDevice,
		TopBrowser:          topBrowser,
		BrowserBreakdown:    browserBreakdown,
		DeviceBreakdown:     deviceBreakdown,
		LanguageBreakdown:   languageBreakdown,
		ScreenBreakdown:     screenBreakdown,
		ReferrerBreakdown:   referrerBreakdown,
		DonutGradient:       donutGradient,
		TopPages:            topPages,
		CountryBreakdown:    countryBreakdown,
		AvgSessionDuration:  avgSessionDuration,
		AvgEventsPerSession: avgEventsPerSession,
		Trend:               trend,
		TrendMax:            trendMax,
		Sessions:            summaries,
		RecentEvents:        recentEvents,
		ActivityPage:        activityPage,
		ActivityTotalPages:  activityTotalPages,
	}
}

// parseEventTimestamp reads the "timestamp" field written by
// static/track.js (new Date().toISOString(), e.g.
// "2026-09-22T10:00:00.000Z") — stored as a plain string, like every other
// non-numeric/boolean/json value (see store.CoerceTyped in jsondata.go).
func parseEventTimestamp(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func breakdownFromCounts(counts map[string]int, total int) []BreakdownEntry {
	out := make([]BreakdownEntry, 0, len(counts))
	for name, count := range counts {
		pct := 0.0
		if total > 0 {
			pct = float64(count) / float64(total) * 100
		}
		out = append(out, BreakdownEntry{Name: name, Count: count, Percent: pct})
	}
	// Simple descending insertion sort — breakdown lists are always small
	// (a handful of browsers/devices/pages), no need for sort.Slice's
	// overhead or import.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Count > out[j-1].Count; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortSessionsByLastSeenDesc(sessions []SessionSummary) {
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0 && sessions[j].LastSeen.After(sessions[j-1].LastSeen); j-- {
			sessions[j], sessions[j-1] = sessions[j-1], sessions[j]
		}
	}
}

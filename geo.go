// geo.go — the platform's offline gazetteer and coordinate extraction.
//
// The README requires that collection pull "entities (people, organisations,
// places, handles, keywords) and coordinates where the item carries them, into
// the graph". stenella has no geocoder and no network lookup for it: the map
// view is explicitly "centered on a low-resolution offline world map", and the
// platform takes no third-party dependency. So coordinates come from two
// honest, offline sources:
//
//  1. explicit lat/lon pairs in the item's own text;
//  2. a small built-in gazetteer of countries, capitals and major cities,
//     matched by word boundary against the item's text.
//
// The table is deliberately short rather than clever. It is a recogniser, not a
// database of places: an unmatched place name still surfaces as an entity, it
// simply has no point on the map. Accuracy of position is about a city, so the
// markers are drawn at that resolution.
package main

import (
	"regexp"
	"sort"
	"strings"
)

type geoPoint struct {
	Lat, Lon float64
}

// gazetteer maps a lowercase place name to its coordinates.
var gazetteer = map[string]geoPoint{
	// --- North America ---------------------------------------------------
	"ottawa":        {45.4215, -75.6972},
	"toronto":       {43.6532, -79.3832},
	"montreal":      {45.5019, -73.5674},
	"vancouver":     {49.2827, -123.1207},
	"calgary":       {51.0447, -114.0719},
	"halifax":       {44.6488, -63.5752},
	"winnipeg":      {49.8951, -97.1384},
	"quebec city":   {46.8139, -71.208},
	"gatineau":      {45.4765, -75.7014},
	"washington":    {38.9072, -77.0369},
	"new york":      {40.7128, -74.006},
	"los angeles":   {34.0522, -118.2437},
	"chicago":       {41.8781, -87.6298},
	"houston":       {29.7604, -95.3698},
	"san francisco": {37.7749, -122.4194},
	"miami":         {25.7617, -80.1918},
	"mexico city":   {19.4326, -99.1332},

	// --- Europe ----------------------------------------------------------
	"london":    {51.5074, -0.1278},
	"paris":     {48.8566, 2.3522},
	"rome":      {41.9028, 12.4964},
	"brussels":  {50.8503, 4.3517},
	"berlin":    {52.52, 13.405},
	"madrid":    {40.4168, -3.7038},
	"moscow":    {55.7558, 37.6173},
	"kyiv":      {50.4501, 30.5234},
	"kiev":      {50.4501, 30.5234},
	"geneva":    {46.2044, 6.1432},
	"vienna":    {48.2082, 16.3738},
	"warsaw":    {52.2297, 21.0122},
	"amsterdam": {52.3676, 4.9041},
	"stockholm": {59.3293, 18.0686},
	"athens":    {37.9838, 23.7275},
	"dublin":    {53.3498, -6.2603},

	// --- Asia-Pacific ----------------------------------------------------
	"beijing":   {39.9042, 116.4074},
	"shanghai":  {31.2304, 121.4737},
	"tokyo":     {35.6762, 139.6503},
	"seoul":     {37.5665, 126.978},
	"delhi":     {28.7041, 77.1025},
	"new delhi": {28.6139, 77.209},
	"singapore": {1.3521, 103.8198},
	"canberra":  {-35.2809, 149.13},
	"sydney":    {-33.8688, 151.2093},
	"manila":    {14.5995, 120.9842},
	"bangkok":   {13.7563, 100.5018},
	"jakarta":   {-6.2088, 106.8456},

	// --- Middle East, Africa, Latin America ------------------------------
	"cairo":        {30.0444, 31.2357},
	"pretoria":     {-25.7479, 28.2293},
	"johannesburg": {-26.2041, 28.0473},
	"nairobi":      {-1.2921, 36.8219},
	"lagos":        {6.5244, 3.3792},
	"sao paulo":    {-23.5505, -46.6333},
	"buenos aires": {-34.6037, -58.3816},
	"santiago":     {-33.4489, -70.6693},
	"bogota":       {4.711, -74.0721},
	"tel aviv":     {32.0853, 34.7818},
	"doha":         {25.2854, 51.531},

	// --- countries and regions -------------------------------------------
	"canada":         {56.1304, -106.3468},
	"united states":  {37.0902, -95.7129},
	"united kingdom": {55.3781, -3.436},
	"france":         {46.2276, 2.2137},
	"italy":          {41.8719, 12.5674},
	"germany":        {51.1657, 10.4515},
	"china":          {35.8617, 104.1954},
	"japan":          {36.2048, 138.2529},
	"india":          {20.5937, 78.9629},
	"russia":         {61.524, 105.3188},
	"ukraine":        {48.3794, 31.1656},
	"australia":      {-25.2744, 133.7751},
	"brazil":         {-14.235, -51.9253},
	"belgium":        {50.5039, 4.4699},
	"poland":         {51.9194, 19.1451},
	"netherlands":    {52.1326, 5.2913},
	"spain":          {40.4637, -3.7492},
	"sweden":         {60.1282, 18.6435},
	"norway":         {60.472, 8.4689},
	"greece":         {39.0742, 21.8243},
	"turkey":         {38.9637, 35.2433},
	"israel":         {31.0461, 34.8516},
	"saudi arabia":   {23.8859, 45.0792},
	"mexico":         {23.6345, -102.5528},
	"argentina":      {-38.4161, -63.6167},
	"south africa":   {-30.5595, 22.9375},
	"nigeria":        {9.082, 8.6753},
	"kenya":          {-0.0236, 37.9062},
	"egypt":          {26.8206, 30.8025},
	"iran":           {32.4279, 53.688},
	"iraq":           {33.2232, 43.6793},
	"albania":        {41.1533, 20.1683},
	"romania":        {45.9432, 24.9668},
	"finland":        {61.9241, 25.7482},
	"denmark":        {56.2639, 9.5018},
	"switzerland":    {46.8182, 8.2275},
	"portugal":       {39.3999, -8.2245},
	"ireland":        {53.4129, -8.2439},
}

var (
	coordPairRe = regexp.MustCompile(`(-?\d{1,3}\.\d+)\s*,\s*(-?\d{1,3}\.\d+)`)
	placeMatch  map[string]*regexp.Regexp
)

func init() {
	// Longest names first, so "new delhi" wins over "delhi" and "united states"
	// over "states" (which is not in the table at all).
	names := make([]string, 0, len(gazetteer))
	for n := range gazetteer {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	placeMatch = make(map[string]*regexp.Regexp, len(names))
	for _, n := range names {
		placeMatch[n] = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(n) + `\b`)
	}
}

// extractGeo finds the coordinates an item carries: an explicit lat/lon pair
// first, otherwise the gazetteer entry it names. ok is false when neither
// yields a point, which is the common case.
func extractGeo(text string) (lat, lon float64, ok bool) {
	if m := coordPairRe.FindStringSubmatch(text); m != nil {
		la, lo, parsed := parseGeo(m[1] + "," + m[2])
		if parsed {
			return la, lo, true
		}
	}
	for name, re := range placeMatch {
		if re.MatchString(text) {
			p := gazetteer[name]
			return p.Lat, p.Lon, true
		}
	}
	return 0, 0, false
}

// placeNames returns every gazetteer place named in text, longest first. It is
// what the entity extractor reports as "place" entities.
func placeNames(text string) []string {
	var out []string
	for name, re := range placeMatch {
		if re.MatchString(text) {
			out = append(out, name)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// displayName renders a gazetteer key (always lowercase) for display.
func displayName(name string) string {
	words := strings.Fields(name)
	for i, w := range words {
		if w == "" {
			continue
		}
		r := []rune(w)
		r[0] = toUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

func toUpper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 'a' + 'A'
	}
	return r
}

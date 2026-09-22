package main

import (
	_ "embed"
	"encoding/binary"
	"net"
	"sort"
)

// geoipData is a sorted array of fixed-width IPv4 range records (10 bytes
// each: 4-byte start, 4-byte end, 2-byte ASCII country code), converted
// once from DB-IP's free "IP to Country Lite" CSV
// (sapics/ip-location-db, dbip-country/dbip-country-ipv4.csv) into a
// compact binary — see geoip/DBIP-LICENSE for the full attribution terms
// (CC BY 4.0: any page displaying results from this data must link back
// to db-ip.com, see the Overview tab's map card).
//
// IPv6 is deliberately not covered: the equivalent dataset is roughly
// twice the size for a small fraction of real traffic in this app's
// context, so an IPv6 visitor's country simply comes back unknown rather
// than doubling the embedded binary for a rare case.
//
//go:embed geoip/dbip_country_ipv4.bin
var geoipData []byte

const geoipRecordSize = 10 // 4 (start) + 4 (end) + 2 (country code)

// lookupCountry resolves an IPv4 address to its ISO 3166-1 alpha-2
// country code. It returns ok=false for IPv6 addresses, addresses outside
// every known range (private/loopback ranges like 127.0.0.1 or 10.x are
// never allocated publicly, so they're simply absent from the dataset),
// or a malformed input — callers treat all of these the same way: an
// "unknown" bucket, not an error.
func lookupCountry(ip net.IP) (code string, ok bool) {
	v4 := ip.To4()
	if v4 == nil {
		return "", false
	}
	target := binary.BigEndian.Uint32(v4)

	numRecords := len(geoipData) / geoipRecordSize
	i := sort.Search(numRecords, func(i int) bool {
		start := binary.BigEndian.Uint32(geoipData[i*geoipRecordSize : i*geoipRecordSize+4])
		return start > target
	})
	// sort.Search found the first record whose start is > target, so the
	// containing range (if any) is the one just before it.
	i--
	if i < 0 {
		return "", false
	}
	rec := geoipData[i*geoipRecordSize : (i+1)*geoipRecordSize]
	end := binary.BigEndian.Uint32(rec[4:8])
	if target > end {
		return "", false
	}
	return string(rec[8:10]), true
}

// countryNames maps ISO 3166-1 alpha-2 codes to their common English name
// — reference data (not copyrightable), used only to label the "top
// countries" legend next to the map.
var countryNames = map[string]string{
	"AD": "Andorra", "AE": "United Arab Emirates", "AF": "Afghanistan", "AG": "Antigua and Barbuda",
	"AI": "Anguilla", "AL": "Albania", "AM": "Armenia", "AO": "Angola", "AQ": "Antarctica",
	"AR": "Argentina", "AS": "American Samoa", "AT": "Austria", "AU": "Australia", "AW": "Aruba",
	"AX": "Åland Islands", "AZ": "Azerbaijan", "BA": "Bosnia and Herzegovina", "BB": "Barbados",
	"BD": "Bangladesh", "BE": "Belgium", "BF": "Burkina Faso", "BG": "Bulgaria", "BH": "Bahrain",
	"BI": "Burundi", "BJ": "Benin", "BL": "Saint Barthélemy", "BM": "Bermuda", "BN": "Brunei",
	"BO": "Bolivia", "BQ": "Bonaire", "BR": "Brazil", "BS": "Bahamas", "BT": "Bhutan", "BV": "Bouvet Island",
	"BW": "Botswana", "BY": "Belarus", "BZ": "Belize", "CA": "Canada", "CC": "Cocos Islands",
	"CD": "DR Congo", "CF": "Central African Republic", "CG": "Congo", "CH": "Switzerland",
	"CI": "Côte d'Ivoire", "CK": "Cook Islands", "CL": "Chile", "CM": "Cameroon", "CN": "China",
	"CO": "Colombia", "CR": "Costa Rica", "CU": "Cuba", "CV": "Cabo Verde", "CW": "Curaçao",
	"CX": "Christmas Island", "CY": "Cyprus", "CZ": "Czechia", "DE": "Germany", "DJ": "Djibouti",
	"DK": "Denmark", "DM": "Dominica", "DO": "Dominican Republic", "DZ": "Algeria", "EC": "Ecuador",
	"EE": "Estonia", "EG": "Egypt", "EH": "Western Sahara", "ER": "Eritrea", "ES": "Spain",
	"ET": "Ethiopia", "FI": "Finland", "FJ": "Fiji", "FK": "Falkland Islands", "FM": "Micronesia",
	"FO": "Faroe Islands", "FR": "France", "GA": "Gabon", "GB": "United Kingdom", "GD": "Grenada",
	"GE": "Georgia", "GF": "French Guiana", "GG": "Guernsey", "GH": "Ghana", "GI": "Gibraltar",
	"GL": "Greenland", "GM": "Gambia", "GN": "Guinea", "GP": "Guadeloupe", "GQ": "Equatorial Guinea",
	"GR": "Greece", "GS": "South Georgia", "GT": "Guatemala", "GU": "Guam", "GW": "Guinea-Bissau",
	"GY": "Guyana", "HK": "Hong Kong", "HM": "Heard Island", "HN": "Honduras", "HR": "Croatia",
	"HT": "Haiti", "HU": "Hungary", "ID": "Indonesia", "IE": "Ireland", "IL": "Israel", "IM": "Isle of Man",
	"IN": "India", "IO": "British Indian Ocean Territory", "IQ": "Iraq", "IR": "Iran", "IS": "Iceland",
	"IT": "Italy", "JE": "Jersey", "JM": "Jamaica", "JO": "Jordan", "JP": "Japan", "KE": "Kenya",
	"KG": "Kyrgyzstan", "KH": "Cambodia", "KI": "Kiribati", "KM": "Comoros", "KN": "Saint Kitts and Nevis",
	"KP": "North Korea", "KR": "South Korea", "KW": "Kuwait", "KY": "Cayman Islands", "KZ": "Kazakhstan",
	"LA": "Laos", "LB": "Lebanon", "LC": "Saint Lucia", "LI": "Liechtenstein", "LK": "Sri Lanka",
	"LR": "Liberia", "LS": "Lesotho", "LT": "Lithuania", "LU": "Luxembourg", "LV": "Latvia", "LY": "Libya",
	"MA": "Morocco", "MC": "Monaco", "MD": "Moldova", "ME": "Montenegro", "MF": "Saint Martin",
	"MG": "Madagascar", "MH": "Marshall Islands", "MK": "North Macedonia", "ML": "Mali", "MM": "Myanmar",
	"MN": "Mongolia", "MO": "Macao", "MP": "Northern Mariana Islands", "MQ": "Martinique",
	"MR": "Mauritania", "MS": "Montserrat", "MT": "Malta", "MU": "Mauritius", "MV": "Maldives",
	"MW": "Malawi", "MX": "Mexico", "MY": "Malaysia", "MZ": "Mozambique", "NA": "Namibia",
	"NC": "New Caledonia", "NE": "Niger", "NF": "Norfolk Island", "NG": "Nigeria", "NI": "Nicaragua",
	"NL": "Netherlands", "NO": "Norway", "NP": "Nepal", "NR": "Nauru", "NU": "Niue", "NZ": "New Zealand",
	"OM": "Oman", "PA": "Panama", "PE": "Peru", "PF": "French Polynesia", "PG": "Papua New Guinea",
	"PH": "Philippines", "PK": "Pakistan", "PL": "Poland", "PM": "Saint Pierre and Miquelon",
	"PN": "Pitcairn", "PR": "Puerto Rico", "PS": "Palestine", "PT": "Portugal", "PW": "Palau",
	"PY": "Paraguay", "QA": "Qatar", "RE": "Réunion", "RO": "Romania", "RS": "Serbia", "RU": "Russia",
	"RW": "Rwanda", "SA": "Saudi Arabia", "SB": "Solomon Islands", "SC": "Seychelles", "SD": "Sudan",
	"SE": "Sweden", "SG": "Singapore", "SH": "Saint Helena", "SI": "Slovenia", "SJ": "Svalbard and Jan Mayen",
	"SK": "Slovakia", "SL": "Sierra Leone", "SM": "San Marino", "SN": "Senegal", "SO": "Somalia",
	"SR": "Suriname", "SS": "South Sudan", "ST": "São Tomé and Príncipe", "SV": "El Salvador",
	"SX": "Sint Maarten", "SY": "Syria", "SZ": "Eswatini", "TC": "Turks and Caicos Islands", "TD": "Chad",
	"TF": "French Southern Territories", "TG": "Togo", "TH": "Thailand", "TJ": "Tajikistan",
	"TK": "Tokelau", "TL": "Timor-Leste", "TM": "Turkmenistan", "TN": "Tunisia", "TO": "Tonga",
	"TR": "Türkiye", "TT": "Trinidad and Tobago", "TV": "Tuvalu", "TW": "Taiwan", "TZ": "Tanzania",
	"UA": "Ukraine", "UG": "Uganda", "UM": "U.S. Minor Outlying Islands", "US": "United States",
	"UY": "Uruguay", "UZ": "Uzbekistan", "VA": "Vatican City", "VC": "Saint Vincent and the Grenadines",
	"VE": "Venezuela", "VG": "British Virgin Islands", "VI": "U.S. Virgin Islands", "VN": "Vietnam",
	"VU": "Vanuatu", "WF": "Wallis and Futuna", "WS": "Samoa", "XK": "Kosovo", "YE": "Yemen",
	"YT": "Mayotte", "ZA": "South Africa", "ZM": "Zambia", "ZW": "Zimbabwe",
}

// countryName returns a human-readable name for the legend, falling back
// to the raw code itself for anything missing from countryNames (a
// handful of rare/reserved codes in the dataset) rather than showing a
// blank label.
func countryName(code string) string {
	if name, ok := countryNames[code]; ok {
		return name
	}
	return code
}

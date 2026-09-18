package main

import (
	"hash/fnv"
	"strings"
)

// ColumnIconName picks a representative icon for a column based on
// keywords in its name — purely cosmetic, to make a grid of columns
// easier to scan at a glance. Falls back to a generic icon.
func ColumnIconName(key string) string {
	lower := strings.ToLower(key)
	for _, kw := range columnIconKeywords {
		for _, w := range kw.words {
			if strings.Contains(lower, w) {
				return kw.icon
			}
		}
	}
	return "layers"
}

var columnIconKeywords = []struct {
	words []string
	icon  string
}{
	{[]string{"where", "location", "address", "city", "country", "from"}, "location"},
	{[]string{"name", "nom", "prenom", "person", "user", "author", "auteur"}, "person"},
	{[]string{"title", "titre", "role", "job", "poste", "position"}, "briefcase"},
	{[]string{"skill", "competence", "tech", "code", "lang"}, "code"},
	{[]string{"email", "mail"}, "mail"},
	{[]string{"phone", "tel"}, "phone"},
	{[]string{"date", "time", "created", "updated", "at"}, "calendar"},
	{[]string{"price", "cost", "amount", "montant", "budget"}, "currency"},
	{[]string{"image", "photo", "avatar", "picture", "logo"}, "image"},
	{[]string{"description", "note", "comment", "bio"}, "text"},
}

// columnColorPalette are the accent hex colors columns cycle through. Chosen
// to read clearly against the app's near-black surfaces.
var columnColorPalette = []string{
	"#2dd4bf", // teal
	"#60a5fa", // blue
	"#c084fc", // purple
	"#fbbf24", // amber
	"#34d399", // green
	"#f472b6", // pink
	"#22d3ee", // cyan
	"#fb923c", // orange
}

// ColumnColorHex deterministically assigns one of columnColorPalette to a
// column, keyed by its name, so the same column always gets the same color
// across reloads without needing to store a choice.
func ColumnColorHex(key string) string {
	h := fnv.New32a()
	h.Write([]byte(key))
	return columnColorPalette[h.Sum32()%uint32(len(columnColorPalette))]
}

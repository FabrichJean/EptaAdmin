package main

import "net/url"

// AvatarURL returns a DiceBear-generated avatar for the given seed (e.g. a
// username), so every user gets a distinct, stable illustrated avatar
// instead of a plain initial letter. The same seed always produces the same
// image.
func AvatarURL(seed string) string {
	return "https://api.dicebear.com/9.x/notionists/svg?seed=" + url.QueryEscape(seed) + "&backgroundType=gradientLinear"
}

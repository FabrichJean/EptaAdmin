package main

import (
	_ "embed"
	"html/template"
)

// worldMapSVG is StephanWagner/svgMap's public-domain-style country map
// (MIT license, see static/world-map.LICENSE.txt), embedded and injected
// raw into the tracking dashboard's Overview tab — inlining it (rather
// than an <img src="...">) is what lets per-country <path id="XX">
// elements be colored by JS based on visit counts.
//
//go:embed static/world-map.svg
var worldMapSVGRaw string

var worldMapSVG = template.HTML(worldMapSVGRaw)

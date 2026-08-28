package tui

const (
	tinyWidth      = 50
	tinyHeight     = 12
	wideWidth      = 100
	overviewWidth  = 30
	maxSuggestions = 6
)

type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type layout struct {
	width, height int
	tiny          bool
	unsupported   bool
	wide          bool
	header        rect
	overview      rect
	transcript    rect
	suggestions   rect
	command       rect
	confirmation  rect
	footer        rect
}

func calculateLayout(width, height, suggestionCount int, confirming bool) layout {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	l := layout{width: width, height: height, tiny: width < tinyWidth || height < tinyHeight, unsupported: width < 32 || height < 8}
	if l.tiny {
		l.header = rect{0, 0, width, 1}
		transcriptTop := 1
		if confirming {
			l.confirmation = rect{0, 1, width, 2}
			transcriptTop = 3
		}
		l.transcript = rect{0, transcriptTop, width, max(1, height-transcriptTop-3)}
		l.command = rect{0, max(1, height-3), width, 2}
		l.footer = rect{0, height - 1, width, 1}
		return l
	}
	l.header = rect{0, 0, width, 3}
	footerHeight := 1
	commandHeight := 3
	suggestionHeight := min(suggestionCount, maxSuggestions)
	if confirming {
		suggestionHeight = 2
	}
	bodyTop := l.header.h
	bottom := height - footerHeight - commandHeight - suggestionHeight
	if bottom <= bodyTop {
		bottom = bodyTop + 1
	}
	l.wide = width >= wideWidth
	if l.wide {
		l.overview = rect{0, bodyTop, overviewWidth, bottom - bodyTop}
		l.transcript = rect{overviewWidth + 1, bodyTop, width - overviewWidth - 1, bottom - bodyTop}
	} else {
		overviewHeight := min(6, max(3, (bottom-bodyTop)/3))
		l.overview = rect{0, bodyTop, width, overviewHeight}
		l.transcript = rect{0, bodyTop + overviewHeight, width, max(1, bottom-bodyTop-overviewHeight)}
	}
	l.suggestions = rect{0, bottom, width, suggestionHeight}
	l.confirmation = l.suggestions
	l.command = rect{0, bottom + suggestionHeight, width, commandHeight}
	l.footer = rect{0, height - footerHeight, width, footerHeight}
	return l
}

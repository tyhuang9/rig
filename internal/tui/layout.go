package tui

const (
	minWidth      = 32
	minHeight     = 8
	compactWidth  = 50
	compactHeight = 12
	wideWidth     = 100
)

type layout struct {
	width, height int
	unsupported   bool
	compact       bool
	wide          bool
	listRows      int
	leftWidth     int
	rightWidth    int
}

func calculateLayout(width, height int) layout {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	l := layout{width: width, height: height, unsupported: width < minWidth || height < minHeight, compact: width <= compactWidth || height <= compactHeight, wide: width >= wideWidth && height > compactHeight}
	switch {
	case l.unsupported:
		l.listRows = 0
	case l.compact:
		l.listRows = max(1, height-5)
	case l.wide:
		l.leftWidth = max(32, width*45/100)
		l.rightWidth = max(1, width-l.leftWidth-2)
		l.listRows = max(1, height-6)
	default:
		l.listRows = max(1, height-12)
	}
	return l
}

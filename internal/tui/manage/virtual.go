package manage

// Window describes the slice of a logical roster that's currently
// being rendered. Lives next to renderRoster's virtualization logic;
// extracted so the orchestrator can keep the scroll math (and the
// keep-focus-in-view invariant) outside the renderer.
type Window struct {
	Total   int // total number of rows in the projection-after-sort+filter
	Visible int // number of rows that fit in the viewport
	Top     int // index of the first visible row
}

// Bottom returns the index just past the last visible row.
func (w Window) Bottom() int {
	end := w.Top + w.Visible
	if end > w.Total {
		end = w.Total
	}
	return end
}

// Contains reports whether idx is currently visible.
func (w Window) Contains(idx int) bool {
	return idx >= w.Top && idx < w.Bottom()
}

// ScrollTo nudges the window so idx is visible. If idx is already
// visible, returns w unchanged. Otherwise pins idx to the top edge
// (when scrolling up) or bottom edge (when scrolling down).
func (w Window) ScrollTo(idx int) Window {
	if w.Visible <= 0 {
		return w
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= w.Total {
		idx = w.Total - 1
	}
	// An empty window (Total == 0) yields idx == -1 above; pinning Top to
	// that would leave a negative scroll origin, which breaks Bottom() /
	// Contains() and the renderer's slice math. Clamp to a valid top.
	if idx < 0 {
		w.Top = 0
		return w
	}
	if idx < w.Top {
		w.Top = idx
		return w
	}
	if idx >= w.Bottom() {
		w.Top = idx - w.Visible + 1
		if w.Top < 0 {
			w.Top = 0
		}
	}
	return w
}

// Clamp ensures w.Top stays within [0, max(Total-Visible, 0)]. Called
// after a refresh that may have shrunk the row count below the
// previous scroll position.
func (w Window) Clamp() Window {
	if w.Visible <= 0 || w.Total <= w.Visible {
		w.Top = 0
		return w
	}
	maxTop := w.Total - w.Visible
	if w.Top > maxTop {
		w.Top = maxTop
	}
	if w.Top < 0 {
		w.Top = 0
	}
	return w
}

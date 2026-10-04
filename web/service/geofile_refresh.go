package service

// geofileRefreshDecision returns whether a completed call should reload Xray and
// carries changes across a failed all-files batch. A successful partial download
// can already be on disk when another file fails; the next full successful pass
// must still reload it even if upstream answers 304 for that file.
func geofileRefreshDecision(fileName string, failed, changed, restartPending, fullRefreshIncomplete bool) (restart, pending, incomplete bool) {
	if changed {
		restartPending = true
	}
	if failed {
		if fileName == "" {
			fullRefreshIncomplete = true
		}
		return false, restartPending, fullRefreshIncomplete
	}
	if fileName == "" {
		fullRefreshIncomplete = false
	}
	if fullRefreshIncomplete {
		return false, restartPending, true
	}
	return restartPending, restartPending, false
}

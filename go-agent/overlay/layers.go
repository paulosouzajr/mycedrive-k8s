package overlay

import "log"

// Layer is the legacy overlay API kept for backwards compatibility.
//
// Deprecated: new code should use LayerManager, which implements the
// five-method VolumeManager API from the CloudCom 2020 paper with proper
// error propagation and numeric layer ordering.
type Layer struct {
	level     int
	RootDir   string
	rootLayer int
	manager   *LayerManager
}

func (l *Layer) lm() *LayerManager {
	if l.manager == nil {
		l.manager = NewLayerManager("/data", l.RootDir)
	}
	return l.manager
}

// CreateLayer freezes the current upper layer and stacks a new writable one.
//
// Deprecated: use LayerManager.CreateCheckpoint, which returns an error.
func (l *Layer) CreateLayer() bool {
	lm := l.lm()
	if lm.Level() == 0 {
		if err := lm.Discover(); err != nil {
			log.Printf("overlay: discover failed: %v", err)
			return false
		}
		if lm.Level() == 0 {
			if err := lm.InitVolume(); err != nil {
				log.Printf("overlay: init failed: %v", err)
				return false
			}
		}
	}
	frozen, err := lm.CreateCheckpoint()
	if err != nil {
		log.Printf("overlay: create checkpoint failed: %v", err)
		return false
	}
	l.level = lm.Level()
	log.Printf("overlay: froze layer %d, new writable level %d", frozen, l.level)
	return true
}

// Init mounts the overlay stack over RootDir.
//
// Deprecated: use LayerManager.InitVolume, which returns an error.
func (l *Layer) Init() bool {
	lm := l.lm()
	if err := lm.InitVolume(); err != nil {
		log.Printf("overlay: init failed: %v", err)
		return false
	}
	l.rootLayer = lm.Level() - 1
	l.level = lm.Level()
	return true
}

// Finish unmounts the overlay stack (no transfer).
//
// Deprecated: use LayerManager.EndVolume, which returns an error.
func (l *Layer) Finish() bool {
	lm := l.lm()
	if lm.Level() == 0 {
		return true // nothing mounted
	}
	if err := lm.EndVolume(""); err != nil {
		log.Printf("overlay: finish failed: %v", err)
		return false
	}
	return true
}

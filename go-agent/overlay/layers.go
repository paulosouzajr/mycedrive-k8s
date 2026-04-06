package overlay

import (
	"fmt"
	"go-agent/utils"
	"log"
	"path/filepath"
	"strings"
)

type Layer struct {
	level     int
	RootDir   string
	rootLayer int
}

func (l *Layer) CreateLayer() bool {
	log.Printf("Creating layer at level: %d over %s\n", l.level, l.RootDir)
	l.level += 1

	lowLayers, err := filepath.Glob("/data/o1*")

	if err != nil {
		log.Printf("Error to read files over /data dir")
		return false
	}

	utils.Run("mkdir", "-p "+fmt.Sprintf("/data/u%d /data/w%d /data/o1%d", l.level, l.level, l.level))

	// If no lower layers exist, use RootDir as the lower directory
	var lowerdir string
	if len(lowLayers) == 0 {
		lowerdir = l.RootDir
	} else {
		lowerdir = strings.Join(lowLayers, ":")
	}

	utils.Run("mount", "-t "+fmt.Sprintf("overlay overlay -o lowerdir=%s,upperdir=/data/u%d,workdir=/data/w%d /data/o1%d", lowerdir, l.level, l.level, l.level))

	utils.Run("umount", "-l "+l.RootDir)

	utils.Run("mount", "--bind "+fmt.Sprintf("/data/o1%d %s", l.level, l.RootDir))

	return true
}

func (l *Layer) Init() bool {
	log.Printf("Init overlay over %s\n", l.RootDir)

	lowLayers, err := filepath.Glob("/data/o1*")

	if err != nil {
		log.Printf("Error to read files over /data dir")
		return false
	}

	l.rootLayer = len(lowLayers)
	l.level = l.rootLayer + 1

	log.Printf("Create new level at : %d over %s\n", l.level, l.RootDir)

	utils.Run("mkdir", "-p "+fmt.Sprintf(" /data/u%d /data/w%d /data/o1%d", l.level, l.level, l.level))

	// If no lower layers exist, use RootDir as the lower directory
	var lowerdir string
	if len(lowLayers) == 0 {
		lowerdir = l.RootDir
	} else {
		lowerdir = strings.Join(lowLayers, ":")
	}

	utils.Run("mount", "-t "+fmt.Sprintf("overlay overlay -o lowerdir=%s,upperdir=/data/u%d,workdir=/data/w%d /data/o1%d", lowerdir, l.level, l.level, l.level))

	return true
}

func (l *Layer) Finish() bool {
	log.Printf("Finish overlay with levels: %d from %s\n", l.level, l.RootDir)

	return true
}

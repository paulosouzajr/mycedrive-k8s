package logs

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

var (
	WarningLogger *log.Logger
	InfoLogger    *log.Logger
	ErrorLogger   *log.Logger
)

func init() {
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		log.Fatal("create log dir:", err)
	}

	sec := time.Now().Unix()
	path := "tmp/" + strconv.FormatInt(sec, 10) + ".log"

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal("open log file:", err)
	}

	flags := log.Ldate | log.Ltime | log.Lshortfile
	InfoLogger = log.New(file, "INFO: ", flags)
	WarningLogger = log.New(file, "WARNING: ", flags)
	ErrorLogger = log.New(file, "ERROR: ", flags)
}

func LogError(err error) {
	if err != nil {
		ErrorLogger.Println(err)
		fmt.Println("ERROR:", err)
	}
}

func LogInfo(s string) {
	InfoLogger.Println(s)
	fmt.Println("INFO:", s)
}

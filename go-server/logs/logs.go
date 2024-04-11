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
	sec := time.Now().Unix()

	file, err := os.OpenFile("tmp/"+strconv.FormatInt(sec, 10)+".log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatal(err)
	}

	InfoLogger = log.New(file, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	WarningLogger = log.New(file, "WARNING: ", log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLogger = log.New(file, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)

	InfoLogger.Println("Init log system output: " + "tmp/" + strconv.FormatInt(sec, 10) + ".log")
}

func LogError(err error) {
	if err != nil {
		ErrorLogger.Println(err.Error())
		fmt.Println("ERROR: ", err)
	}
}

func LogInfo(s string) {
	InfoLogger.Println(s)
	fmt.Println("INFO: ", s)
}

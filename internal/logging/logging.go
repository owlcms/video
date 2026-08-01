package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

var (
	InfoLogger    = log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	WarningLogger = log.New(os.Stdout, "WARN: ", log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLogger   = log.New(os.Stderr, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
	logFile       *os.File
	logDir        string
	Verbose       bool // Move Verbose flag here from config package
)

// Trace logs a debug message that only appears when verbose logging is enabled
func Trace(format string, v ...interface{}) {
	if Verbose {
		InfoLogger.Printf(format, v...)
	}
}

// SetVerbose sets the verbose logging flag
func SetVerbose(verbose bool) {
	Verbose = verbose
}

// Init initializes the loggers
func Init(logDirectory string) error {
	return InitWithFile(logDirectory, "replays.log")
}

// InitWithFile initializes the loggers with a custom log file name
func InitWithFile(logDirectory, logFileName string) error {
	logDir = logDirectory

	// Create logs directory if it doesn't exist
	if err := os.MkdirAll(logDir, os.ModePerm); err != nil {
		fmt.Printf("Failed to create log directory: %v\n", err)
		return err
	}

	// Open log file with O_SYNC to ensure no buffering
	var err error
	logFile, err = os.OpenFile(filepath.Join(logDir, logFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_SYNC, 0666)
	if err != nil {
		return err
	}

	// Initialize writers based on platform
	var infoWriter, warnWriter, errorWriter io.Writer
	if hasConsole() {
		infoWriter = io.MultiWriter(os.Stdout, logFile)
		warnWriter = io.MultiWriter(os.Stdout, logFile)
		errorWriter = io.MultiWriter(os.Stderr, logFile)
	} else {
		infoWriter = logFile
		warnWriter = logFile
		errorWriter = logFile
	}

	// Initialize loggers with timestamps and source file info
	flags := log.Ldate | log.Ltime | log.Lshortfile
	InfoLogger = log.New(infoWriter, "INFO: ", flags)
	WarningLogger = log.New(warnWriter, "WARN: ", flags)
	ErrorLogger = log.New(errorWriter, "ERROR: ", flags)

	return nil
}

func hasConsole() bool {
	stdoutHasConsole := fileHasConsole(os.Stdout)
	stderrHasConsole := fileHasConsole(os.Stderr)
	return stdoutHasConsole || stderrHasConsole
}

func fileHasConsole(f *os.File) bool {
	if f == nil {
		return false
	}

	stat, err := f.Stat()
	if err != nil {
		return false
	}

	return (stat.Mode() & os.ModeCharDevice) != 0
}

// Close closes the log file
func Close() {
	if logFile != nil {
		logFile.Close()
	}
}

package metakill_test

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/computerguymackay/metakill"
)

func ExampleCleanFile() {
	rep, err := metakill.CleanFile("holiday.jpg", metakill.Options{})
	switch {
	case errors.Is(err, metakill.ErrUnknownFormat), errors.Is(err, metakill.ErrSkipped):
		return // not something metakill cleans
	case err != nil:
		log.Fatal(err)
	}
	fmt.Printf("%s: %s, %d fields removed\n", rep.Path, rep.Status, rep.TotalFields())
	if !rep.FullyClean {
		fmt.Println("not fully clean:", rep.Warnings)
	}
}

func ExampleCleanBytes() {
	data, err := os.ReadFile("upload.jpg")
	if err != nil {
		log.Fatal(err)
	}
	clean, rep, err := metakill.CleanBytes(data, "upload.jpg", metakill.Options{KeepICC: true})
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range rep.Fields {
		fmt.Println("removed", f.Category, f.Namespace+":"+f.Key)
	}
	_ = clean // store or serve the cleaned bytes
}

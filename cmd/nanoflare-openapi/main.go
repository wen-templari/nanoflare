package main

import (
	"fmt"
	"os"

	"github.com/clas/nanoflare/internal/api"
)

func main() {
	data, err := api.OpenAPIJSON()
	if err != nil {
		panic(err)
	}
	data = append(data, '\n')
	if len(os.Args) > 1 {
		if err := os.WriteFile(os.Args[1], data, 0o644); err != nil {
			panic(err)
		}
		return
	}
	_, _ = fmt.Print(string(data))
}

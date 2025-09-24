# github.com/pgavlin/glob

Package glob provides utilities for matching patterns against filesystem trees.

The matcher is designed to read as few directories as possible while matching; only directories that may contain
matches are considered.

```go
package main

import (
    "fmt"
    "log"
    "os"

    "github.com/pgavlin/glob"
)

func main() {
    glob, err := glob.New([]string{os.Args[1]}, []string{os.Args[2]})
    if err != nil {
        log.Fatal(err)
    }
    for path, err := range glob.Match(os.DirFS("."), ".", true) {
        if err != nil {
            log.Fatalf("%v: %v", path, err)
        }
        fmt.Println(path)
    }
}
```

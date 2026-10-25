package fetch

import (
	"net/http"
	"time"
)

func FirstOK(urls []string) string {
	ch := make(chan string)
	for _, u := range urls {
		go func(u string) {
			resp, err := http.Get(u)
			if err != nil {
				return
			}
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ch <- u
			}
		}(u)
	}
	select {
	case u := <-ch:
		return u
	case <-time.After(2 * time.Second):
		return ""
	}
}

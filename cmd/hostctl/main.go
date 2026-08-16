package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	var endpoint, sessionToken string
	root := &cobra.Command{Use: "hostctl", Short: "hostd local control-plane client"}
	root.PersistentFlags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:7345", "hostd endpoint")
	root.PersistentFlags().StringVar(&sessionToken, "session-token", os.Getenv("HOSTD_SESSION_TOKEN"), "hostd browser session token")
	get := func(path string) func(*cobra.Command, []string) {
		return func(_ *cobra.Command, _ []string) {
			req, err := http.NewRequest(http.MethodGet, endpoint+path, nil)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return
			}
			if sessionToken != "" {
				req.AddCookie(&http.Cookie{Name: "hostd_session", Value: sessionToken})
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return
			}
			defer r.Body.Close()
			var v any
			if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return
			}
			b, _ := json.MarshalIndent(v, "", "  ")
			fmt.Println(string(b))
		}
	}
	root.AddCommand(&cobra.Command{Use: "status", Run: get("/api/v1/system/status")}, &cobra.Command{Use: "doctor", Run: get("/api/v1/system/doctor")})
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

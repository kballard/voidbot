package urls

import (
	"../"
	"../command"
	"../database"
	"database/sql"
	"fmt"
	"github.com/kballard/gocallback/callback"
	"github.com/kballard/goirc/irc"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

var URLRegex = regexp.MustCompile("(?i)\\b((?:[a-z][\\w-]+:(?:/{1,3}|[a-z0-9%])|www\\d{0,3}[.]|[a-z0-9.\\-]+[.][a-z]{2,4}/)(?:[^\\s()<>]+|\\(([^\\s()<>]+|(\\([^\\s()<>]+\\)))*\\))+(?:\\(([^\\s()<>]+|(\\([^\\s()<>]+\\)))*\\)|[^\\s`!()\\[\\]{};:'\".,<>?«»“”‘’]))")

var historyDB *sql.DB

func init() {
	plugin.RegisterPlugin("urls", plugin.Callbacks{Init: setupURLs, Teardown: teardownURLs})
}

func setupURLs(reg *callback.Registry, config map[string]interface{}) error {
	var err error
	historyDB, err = database.Open("sqlite3", "./history.db")
	if err != nil {
		return err
	}

	sqls := []string{
		"CREATE TABLE IF NOT EXISTS seen (id integer not null primary key, url text not null, nick text, src text not null, dst text not null, timestamp datetime not null)",
		"CREATE INDEX IF NOT EXISTS url_idx ON seen (url, dst)",
	}
	for _, sqlstr := range sqls {
		_, err = historyDB.Exec(sqlstr)
		if err != nil {
			return err
		}
	}

	reg.AddCallback("PRIVMSG", func(conn *irc.Conn, line irc.Line, dst, text string) {
		matches := URLRegex.FindAllStringSubmatch(text, -1)
		if matches != nil {
			for _, submatches := range matches {
				urlStr := submatches[1]
				if u, err := url.Parse(urlStr); err == nil && u.Host != "" {
					reg.Dispatch("URL", conn, line, dst, u)
				}
			}
		}
	})

	reg.AddCallback("URL", func(conn *irc.Conn, line irc.Line, dst string, url *url.URL) {
		handleURL(conn, historyDB, line, dst, url)
	})

	reg.AddCallback("COMMAND", func(conn *irc.Conn, line irc.Line, cmd, arg, dst string, isPrivate bool) {
		if cmd == "urls" {
			handleCommand(conn, historyDB, line, arg, dst, isPrivate)
		}
	})

	return nil
}

func teardownURLs() error {
	if historyDB != nil {
		err := database.Close(historyDB)
		historyDB = nil
		return err
	}
	return nil
}

func handleURL(conn *irc.Conn, db *sql.DB, line irc.Line, dst string, url *url.URL) {
	tx, err := db.Begin()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer func() {
		sqlstr := "INSERT INTO seen (url, nick, src, dst, timestamp) VALUES (?, ?, ?, ?, ?)"
		_, err := tx.Exec(sqlstr, url.String(), line.Src.Nick, line.Src.Raw, dst, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "%q: %s\n", err, sqlstr)
			tx.Rollback()
		} else {
			err = tx.Commit()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
	}()

	sqlstr := "SELECT nick, src, timestamp FROM seen WHERE url = ? AND dst = ? ORDER BY id DESC LIMIT 1"
	row := tx.QueryRow(sqlstr, url.String(), dst)

	var nick, src string
	var timestamp time.Time
	err = row.Scan(&nick, &src, &timestamp)
	if err != sql.ErrNoRows {
		if err != nil && err != sql.ErrNoRows {
			fmt.Fprintf(os.Stderr, "%q: %s\n", err, sqlstr)
			return
		}

		if nick == "" {
			nick = src
		}

		sqlstr = "SELECT COUNT(*) FROM seen WHERE url = ? AND dst = ?"
		row = tx.QueryRow(sqlstr, url.String(), dst)

		var count int
		err = row.Scan(&count)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}

		now := time.Now()
		delta := now.Sub(timestamp)
		lastSeen := formatDuration(delta)

		msg := fmt.Sprintf("URL '%s' was last seen %s ago by %s (%d total)", url, lastSeen, nick, count)
		plugin.Conn(conn).Notice(dst, msg)
	}
}

func handleCommand(conn *irc.Conn, db *sql.DB, line irc.Line, arg, dst string, isPrivate bool) {
	if !isPrivate {
		conn.Notice(dst, "urls: URL querying must be done over private messages")
		return
	}

	arg = strings.TrimSpace(arg)

	if arg == "help" {
		conn.Notice(dst, fmt.Sprintf("urls: usage: %surls", command.CommandPrefix))
		conn.Notice(dst, "urls: Prints the last 5 URLs seen in all channels")
		return
	}

	sqlstr := "SELECT nick, src, timestamp, dst, url FROM seen GROUP BY url ORDER BY id DESC LIMIT ?"
	n := 5
	rows, err := db.Query(sqlstr, n)

	if err != nil {
		fmt.Println("error in !urls:", err)
		conn.Notice(dst, "urls: Internal error occurred")
		return
	}

	for rows.Next() {
		reply := dst
		var nick, src, dst, url string
		var timestamp time.Time
		if err = rows.Scan(&nick, &src, &timestamp, &dst, &url); err != nil {
			fmt.Println("error in !urls:", err)
			conn.Notice(dst, "urls: Internal error occurred")
			rows.Close()
			return
		}
		if nick == "" {
			nick = src
		}
		timestr := timestamp.Format("01-02 15:04:05")
		conn.Notice(reply, fmt.Sprintf("%s: %s: %s by %s", timestr, dst, url, nick))
		n -= 1
	}

	if n > 0 {
		conn.Notice(dst, "(no more URLs)")
	}
}

func formatDuration(d time.Duration) string {
	h := int64(d.Hours())
	if h >= 24 {
		days := h / 24
		return pluralize(days, "day")
	} else if h >= 1 {
		return pluralize(h, "hour")
	}
	m := int64(d.Minutes())
	if m >= 1 {
		return pluralize(m, "minute")
	}
	s := int64(d.Seconds())
	return pluralize(s, "second")
}

func pluralize(count int64, text string) string {
	if count > 1 {
		return fmt.Sprintf("%d %ss", count, text)
	}
	return fmt.Sprintf("%d %s", count, text)
}

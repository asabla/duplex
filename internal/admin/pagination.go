package admin

import (
	"net/url"
	"strconv"
)

type pagination struct {
	Filters                                 url.Values
	Label, Path, Query                      string
	Number, Pages, Size, Total, From, To    int
	FirstURL, PreviousURL, NextURL, LastURL string
	requested                               int
}

func paginate(values url.Values, path, label string, total int) pagination {
	number, _ := strconv.Atoi(values.Get("page"))
	number = max(1, number)
	size, _ := strconv.Atoi(values.Get("per_page"))
	if size != 25 && size != 50 && size != 100 {
		size = 25
	}
	p := pagination{Filters: values, Label: label, Path: path, Query: values.Get("q"), Size: size, Total: total, requested: number}
	p.Pages = max(1, (total+size-1)/size)
	p.Number = min(number, p.Pages)
	if total > 0 {
		p.From = p.start() + 1
		p.To = p.end()
	}
	link := func(n int) string {
		q := url.Values{}
		for key, values := range values {
			q[key] = append([]string(nil), values...)
		}
		q.Set("page", strconv.Itoa(n))
		q.Set("per_page", strconv.Itoa(size))
		if p.Query != "" {
			q.Set("q", p.Query)
		}
		return path + "?" + q.Encode()
	}
	if p.Number > 1 {
		p.FirstURL, p.PreviousURL = link(1), link(p.Number-1)
	}
	if p.Number < p.Pages {
		p.NextURL, p.LastURL = link(p.Number+1), link(p.Pages)
	}
	return p
}

func (p pagination) start() int { return (p.Number - 1) * p.Size }
func (p pagination) end() int   { return min(p.start()+p.Size, p.Total) }

func (p pagination) FragmentURL() string {
	q := url.Values{"page": {strconv.Itoa(p.Number)}, "per_page": {strconv.Itoa(p.Size)}}
	return "/fragments/channels?" + q.Encode()
}

func sortLink(values url.Values, path, order string) string {
	query := url.Values{}
	for key, items := range values {
		query[key] = append([]string(nil), items...)
	}
	query.Set("sort", order)
	query.Del("page")
	return path + "?" + query.Encode()
}

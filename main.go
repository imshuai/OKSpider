package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sjson "github.com/bitly/go-simplejson"
	"github.com/gomodule/redigo/redis"
)

const (
	entryList   = "https://api.okzy.tv/api.php/provide/vod/at/json/?ac=list&pg="
	entryDetail = "https://api.okzy.tv/api.php/provide/vod/at/json/?ac=detail&ids="
	timeOut     = time.Second * 30
)

var (
	pgCount    int
	totalCount int
	vidChan    chan uint64
	vodChan    chan *vod
	//db          *bolt.DB
	rPool       *redis.Pool
	ignoreTypes = []string{
		"资讯",
		"公告",
		"头条",
		"福利片",
		"解说",
		"电影解说",
	}
)

type tLimit struct {
	N int
	L *sync.RWMutex
}

type tArrStrings []string

func (t *tArrStrings) MarshalJSON() (text []byte, err error) {
	var tt []string
	for _, s := range *t {
		s = `"` + s + `"`
		tt = append(tt, s)
	}
	text = []byte("[" + strings.Join(tt, ",") + "]")
	return text, nil
}
func (t *tArrStrings) UnmarshalJSON(text []byte) error {
	s := string(text)
	s = strings.TrimRight(strings.TrimLeft(s, "["), "]")
	ss := strings.Split(s, ",")
	*t = make(tArrStrings, 0)
	for _, tt := range ss {
		tt = strings.Trim(tt, "\"")
		*t = append(*t, tt)
	}
	return nil
}

func (t *tArrStrings) encode() []byte {
	byts, _ := t.MarshalJSON()
	return byts
}
func (t *tArrStrings) RedisScan(src interface{}) error {
	switch src := src.(type) {
	case string:
		json.Unmarshal([]byte(src), t)
	case []byte:
		json.Unmarshal(src, t)
	default:
		return fmt.Errorf("error type of src[%v]", src)
	}
	return nil
}

type tURLs map[string]tArrStrings

func (t *tURLs) encode() []byte {
	tt, _ := json.Marshal(t)
	return tt
}
func (t *tURLs) RedisScan(src interface{}) error {
	switch src := src.(type) {
	case string:
		json.Unmarshal([]byte(src), t)
	case []byte:
		json.Unmarshal(src, t)
	default:
		return fmt.Errorf("error type of src[%v]", src)
	}
	return nil
}

type vod struct {
	ID         uint64      `json:"vod_id" redis:"id"`
	Name       string      `json:"vod_name" redis:"name"`
	Class      string      `json:"vod_class" redis:"class"`
	Pic        string      `json:"vod_pic" redis:"pic"`
	Actors     tArrStrings `json:"vod_actor" redis:"actors"`
	Director   string      `json:"vod_director" redis:"director"`
	Content    string      `json:"vod_content" redis:"content"`
	UpdateTime string      `json:"vod_time" redis:"updatetime"`
	Area       string      `json:"vod_area" redis:"area"`
	Language   string      `json:"vod_lang" redis:"language"`
	Year       string      `json:"vod_year" redis:"year"`
	Remarks    string      `json:"vod_remarks" redis:"remarks"`
	PlayURL    tURLs       `json:"vod_play_url" redis:"playurl"`
	DownURL    tURLs       `json:"vod_down_url" redis:"downurl"`
}

func (v *vod) encode() []byte {
	byts, _ := json.Marshal(v)
	return byts
}

func (v *vod) decode(byts []byte) {
	err := json.Unmarshal(byts, v)
	if err != nil {
		return
	}
}

func (v *vod) key() string {
	return "vid:" + strconv.FormatUint(v.ID, 10)
}

func init() {
	vidChan = make(chan uint64, 10)
	vodChan = make(chan *vod, 50)
	rPool = redis.NewPool(func() (redis.Conn, error) {
		conn, err := redis.Dial("tcp", "192.168.1.2:6379", redis.DialConnectTimeout(time.Second*1), redis.DialDatabase(0), redis.DialKeepAlive(time.Second*10))
		if err != nil {
			return nil, err
		}
		return conn, nil
	}, 3)
}

func main() {
	defer rPool.Close()
	resp, err := get(entryList + "1")
	if err != nil {
		log.Println(err)
		return
	}
	js, err := sjson.NewJson(resp)
	if err != nil {
		log.Println(err)
		return
	}
	if code, _ := js.Get("code").Int(); code != 1 {
		log.Println(js.Get("msg").String())
		return
	}
	pgCount, _ = js.Get("pagecount").Int()
	totalCount, _ = js.Get("total").Int()
	log.Printf("page count:%d\ttotal count:%d\n", pgCount, totalCount)
	go detail(vidChan)
	go func() {
		for pg := 0; pg < pgCount; pg++ {
			list(pg + 1)
		}
	}()
	save(vodChan)
}

func list(pg int) {
	log.Printf("start get vod list page %d\n", pg)
	resp, err := get(entryList + strconv.Itoa(pg))
	if err != nil {
		log.Println(err)
		return
	}
	js, err := sjson.NewJson(resp)
	if err != nil {
		log.Println(err)
		return
	}
	for idx := 0; idx < 25; idx++ {
		var isIgnored = false
		typeName := js.Get("list").GetIndex(idx).Get("type_name").MustString()
		for _, igType := range ignoreTypes {
			if typeName == igType {
				isIgnored = true
				break
			}
		}
		if isIgnored {
			continue
		}
		vid := js.Get("list").GetIndex(idx).Get("vod_id").MustUint64()
		v := &vod{
			ID: vid,
		}
		updateTime, _ := time.Parse("2006-01-02 15:04:05", js.Get("list").GetIndex(idx).Get("vod_time").MustString())
		var isUpdated bool
		var lastUpdateTime time.Time
		conn := rPool.Get()
		defer conn.Close()
		s, _ := redis.String(conn.Do("HGET", v.key(), "updatetime"))
		lastUpdateTime, _ = time.Parse("2006-01-02 15:04:05", s)
		isUpdated = updateTime.After(lastUpdateTime)
		if isUpdated {
			vidChan <- vid
		} else {
			log.Printf("ignore vod[%d], last update time[%v] but this time[%v]\n", vid, lastUpdateTime, updateTime)
		}

	}
}

func detail(ch <-chan uint64) {
	log.Printf("start to get vod detail, wait for vid form vidChan\n")
	limit := &tLimit{
		N: 5,
		L: &sync.RWMutex{},
	}
	for vid := range ch {
		limit.L.Lock()
		limit.N--
		limit.L.Unlock()
		for {
			if limit.N >= 0 {
				break
			}
			time.Sleep(time.Second * 1)
		}
		tid := 5 - limit.N
		go func(vid uint64, tid int) {
			resp, err := get(entryDetail + strconv.FormatInt(int64(vid), 10))
			if err != nil {
				log.Println(err)
				return
			}
			js, err := sjson.NewJson(resp)
			if err != nil {
				log.Println(err)
				return
			}
			vod := &vod{}
			jvod := js.Get("list").GetIndex(0)
			vod.ID = jvod.Get("vod_id").MustUint64()
			vod.Name = jvod.Get("vod_name").MustString()
			vod.Class = jvod.Get("vod_class").MustString()
			vod.Pic = jvod.Get("vod_pic").MustString()
			vod.Actors = func() tArrStrings {
				ss := tArrStrings(strings.Split(jvod.Get("vod_actor").MustString(), ","))
				return ss
			}()
			vod.Director = jvod.Get("vod_director").MustString()
			vod.Content = jvod.Get("vod_content").MustString()
			vod.UpdateTime = jvod.Get("vod_time").MustString()
			vod.Area = jvod.Get("vod_area").MustString()
			vod.Language = jvod.Get("vod_lang").MustString()
			vod.Year = jvod.Get("vod_year").MustString()
			vod.Remarks = jvod.Get("vod_remarks").MustString()
			vod.PlayURL = func() tURLs {
				playURL := make(tURLs)
				str := strings.ReplaceAll(jvod.Get("vod_play_url").MustString(), "$$$", "#")
				urls := strings.Split(str, "#")
				for _, url := range urls {
					t := strings.Split(url, "$")
					if len(t) == 2 {
						playURL[t[0]] = append(playURL[t[0]], t[1])
					} else if len(t) == 1 {
						if strings.HasPrefix(t[0], "http") {
							playURL[vod.Name] = append(playURL[vod.Name], t[0])
						} else {
							reg, _ := regexp.Compile("https?://[-A-Za-z0-9+&@#/%?=~_|!:,.;/]+.+(.m3u8|)")
							s := reg.FindString(t[0])
							if s != "" && strings.HasPrefix(s, "http") {
								playURL[vod.Name] = append(playURL[vod.Name], s)
							}
						}
					} else {
						log.Printf("get vod[%d] play urls faild, url[%s]\n", vod.ID, url)
					}
				}
				return playURL
			}()
			vod.DownURL = func() tURLs {
				downURL := make(tURLs)
				str := strings.ReplaceAll(jvod.Get("vod_down_url").MustString(), "$$$", "#")
				urls := strings.Split(str, "#")
				for _, url := range urls {
					t := strings.Split(url, "$")
					if len(t) == 2 {
						downURL[t[0]] = append(downURL[t[0]], t[1])
					} else if len(t) == 1 {
						if strings.HasPrefix(t[0], "http") {
							downURL[vod.Name] = append(downURL[vod.Name], t[0])
						} else {
							reg, _ := regexp.Compile("https?://[-A-Za-z0-9+&@#/%?=~_|!:,.;/]+.+\\.(mp4|mkv|avi|rmvb)")
							s := reg.FindString(t[0])
							if s != "" && strings.HasPrefix(s, "http") {
								downURL[vod.Name] = append(downURL[vod.Name], s)
							}
						}
					} else {
						log.Printf("get vod[%d] download urls faild, url[%s]\n", vod.ID, url)
					}
				}
				return downURL
			}()
			vodChan <- vod
			limit.L.Lock()
			limit.N++
			limit.L.Unlock()
		}(vid, tid)
	}
}

func save(ch <-chan *vod) {
	for {
		ticker := time.NewTicker(timeOut)
		select {
		case vod := <-ch:
			conn := rPool.Get()
			defer conn.Close()
			key := "vid:" + strconv.FormatUint(vod.ID, 10)
			conn.Send("HMSET", key, "id", vod.ID, "name", vod.Name)
			conn.Send("HMSET", key, "class", vod.Class, "pic", vod.Pic)
			conn.Send("HMSET", key, "actors", vod.Actors.encode(), "director", vod.Director)
			conn.Send("HMSET", key, "content", vod.Content, "updatetime", vod.UpdateTime)
			conn.Send("HMSET", key, "area", vod.Area, "language", vod.Language)
			conn.Send("HMSET", key, "year", vod.Year, "remarks", vod.Remarks)
			conn.Send("HMSET", key, "playurl", vod.PlayURL.encode(), "downurl", vod.DownURL.encode())
			_, err := conn.Do("")
			if err != nil {
				log.Println("update redis", err)
			}
			conn.Close()
		case <-ticker.C:
			log.Printf("%v timeout, exit.\n", timeOut)
			return
		}
	}
}

func get(url string) ([]byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/69.0.3497.100 Safari/537.36")
	req.Header.Set("Cache-Control", "max-age=0")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)
}

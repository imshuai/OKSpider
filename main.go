package main

import (
	"encoding/binary"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sjson "github.com/bitly/go-simplejson"
	"github.com/boltdb/bolt"
)

const (
	entryList   = "https://api.okzy.tv/api.php/provide/vod/at/json/?ac=list&pg="
	entryDetail = "https://api.okzy.tv/api.php/provide/vod/at/json/?ac=detail&ids="
)

var (
	pgCount     int
	totalCount  int
	vidChan     chan uint64
	vodChan     chan *vod
	db          *bolt.DB
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

type vod struct {
	ID         uint64              `json:"vod_id"`
	Name       string              `json:"vod_name"`
	Class      string              `json:"vod_class"`
	Pic        string              `json:"vod_pic"`
	Actors     []string            `json:"vod_actor"`
	Director   string              `json:"vod_director"`
	Content    string              `json:"vod_content"`
	UpdateTime string              `json:"vod_time"`
	Area       string              `json:"vod_area"`
	Language   string              `json:"vod_lang"`
	Year       string              `json:"vod_year"`
	Remarks    string              `json:"vod_remarks"`
	PlayURL    map[string][]string `json:"vod_play_url"`
	DownURL    map[string][]string `json:"vod_down_url"`
}

func (v *vod) encode() []byte {
	byts, _ := json.Marshal(v)
	return byts
}

func (v *vod) decode(byts []byte) {
	t := v
	err := json.Unmarshal(byts, t)
	if err != nil {
		return
	}
	v = t
}

func (v *vod) key() []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, v.ID)
	return buf
}

func init() {
	var err error
	vidChan = make(chan uint64, 10)
	vodChan = make(chan *vod, 20)
	opt := bolt.DefaultOptions
	opt.Timeout = time.Second * 1
	opt.NoGrowSync = true
	db, err = bolt.Open("vods.db", 0600, opt)
	if err != nil {
		log.Fatalln(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("vods"))
		return err
	}); err != nil {
		log.Fatal(err)
	}
}

func main() {
	defer db.Close()
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
		if err := db.View(func(tx *bolt.Tx) error {
			bck := tx.Bucket([]byte("vods"))
			v.decode(bck.Get(v.key()))
			lastUpdateTime, _ := time.Parse("2006-01-02 15:04:05", v.UpdateTime)
			isUpdated = updateTime.After(lastUpdateTime)
			return nil
		}); err != nil {
			log.Println("update", err)
		}
		if isUpdated {
			//log.Printf("send vod[%d] to get detail\n", vid)
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
			//log.Printf("thread %d: start to get vod[%d] detail\n", tid, vid)
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
			vod.Actors = strings.Split(jvod.Get("vod_actor").MustString(), ",")
			vod.Director = jvod.Get("vod_director").MustString()
			vod.Content = jvod.Get("vod_content").MustString()
			vod.UpdateTime = jvod.Get("vod_time").MustString()
			vod.Area = jvod.Get("vod_area").MustString()
			vod.Language = jvod.Get("vod_lang").MustString()
			vod.Year = jvod.Get("vod_year").MustString()
			vod.Remarks = jvod.Get("vod_remarks").MustString()
			vod.PlayURL = func() map[string][]string {
				playURL := make(map[string][]string)
				str := strings.ReplaceAll(jvod.Get("vod_play_url").MustString(), "$$$", "#")
				urls := strings.Split(str, "#")
				for _, url := range urls {
					t := strings.Split(url, "$")
					if len(t) == 2 {
						playURL[t[0]] = append(playURL[t[0]], t[1])
					} else if len(t) == 1 && strings.HasPrefix(t[0], "http") {
						playURL[vod.Name] = append(playURL[vod.Name], t[0])
					} else {
						log.Printf("get vod[%d] play urls faild, url[%s]\n", vod.ID, url)
					}
				}
				return playURL
			}()
			vod.DownURL = func() map[string][]string {
				downURL := make(map[string][]string)
				str := strings.ReplaceAll(jvod.Get("vod_down_url").MustString(), "$$$", "#")
				urls := strings.Split(str, "#")
				for _, url := range urls {
					t := strings.Split(url, "$")
					if len(t) == 2 {
						downURL[t[0]] = append(downURL[t[0]], t[1])
					} else if len(t) == 1 && strings.HasPrefix(t[0], "http") {
						downURL[vod.Name] = append(downURL[vod.Name], t[0])
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
		ticker := time.NewTicker(time.Second * 30)
		select {
		case vod := <-ch:
			if err := db.Update(func(tx *bolt.Tx) error {
				bck := tx.Bucket([]byte("vods"))
				err := bck.Put(vod.key(), vod.encode())
				if err != nil {
					log.Printf("PUT %s fail with error:%v\n", vod.Name, err)
					return err
				}
				//log.Printf("save vod[%s] success\n", vod.Name)
				return nil
			}); err != nil {
				log.Println("update", err)
			}
		case <-ticker.C:
			log.Println("30s timeout, exit.")
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

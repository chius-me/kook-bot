package tts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"unicode/utf8"
)

const (
	cacheRunes = 15
	cacheFiles = 200
)

// CacheKey hashes provider, voice, rate, and text. Volume is not part of the key.
func CacheKey(provider, voice string, rate float64, text string) string {
	sum := sha256.Sum256([]byte(provider + "\n" + voice + "\n" + strconv.FormatFloat(rate, 'f', -1, 64) + "\n" + text))
	return hex.EncodeToString(sum[:])
}

// Cacheable is true for sentences of at most 15 runes.
func Cacheable(text string) bool {
	return utf8.RuneCountInString(text) > 0 && utf8.RuneCountInString(text) <= cacheRunes
}

func CachePath(dir, key string) string {
	return filepath.Join(dir, key+".mp3")
}

// PruneCache keeps the newest max files by mtime.
func PruneCache(dir string, max int) error {
	if max <= 0 {
		max = cacheFiles
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type file struct {
		path string
		mod  int64
	}
	var files []file
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".mp3" {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		files = append(files, file{path: filepath.Join(dir, ent.Name()), mod: info.ModTime().UnixNano()})
	}
	if len(files) <= max {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	for _, f := range files[:len(files)-max] {
		_ = os.Remove(f.path)
	}
	return nil
}

package mapcuration

import (
	"sort"
	"strings"
)

type Region struct {
	Key         string
	Description string
}

var regions = []Region{
	{Key: "football", Description: "Futbol kulüpleri, oyuncular, transferler, ligler ve milli takımlar."},
	{Key: "other_sports", Description: "Futbol dışı sporlar ve sporcular."},
	{Key: "turkish_politics", Description: "Türkiye siyaseti, partiler, liderler ve yerel siyaset."},
	{Key: "world_politics", Description: "Uluslararası siyaset, savaşlar, ülkeler ve dış politika."},
	{Key: "relationships", Description: "Romantik ilişkiler, flört, evlilik, cinsellik ve toplumsal cinsiyet."},
	{Key: "daily_life", Description: "Gündelik hayat, kişisel deneyimler, alışkanlıklar, sorunsallar ve yaşam tavsiyeleri."},
	{Key: "music", Description: "Müzik, şarkılar, sanatçılar ve müzik paylaşımı."},
	{Key: "film_tv", Description: "Film, dizi, televizyon programları, ünlüler ve popüler eğlence."},
	{Key: "games_tech", Description: "Video oyunları, teknoloji, internet ürünleri ve yapay zeka."},
	{Key: "economy", Description: "Ekonomi, finans, piyasalar, tüketim ve iş hayatı."},
	{Key: "culture_art", Description: "Kitaplar, şiir, tarih, sanat, felsefe ve akademi."},
	{Key: "society_identity", Description: "Toplumsal kimlik, din, göç, etik, kültürel tartışmalar ve kolektif meseleler."},
	{Key: "science_health", Description: "Bilim, sağlık, eğitim ve pratik bilgi."},
	{Key: "local_life", Description: "Şehirler, mekanlar, seyahat, yerel yaşam ve hava durumu."},
	{Key: "media", Description: "Haber kuruluşları, yayıncılar, gazeteciler ve medya kişilikleri."},
	{Key: "news_events", Description: "Belirli güncel olaylar; yalnızca başka bir bölge daha açıklayıcı değilse."},
	{Key: "other", Description: "Güvenli biçimde sınıflandırılamayan veya karma kümeler."},
}

// Formats the canonical region taxonomy for reconciliation prompts.
func RegionDefinitions() string {
	var definitions strings.Builder
	for _, region := range regions {
		definitions.WriteString("- ")
		definitions.WriteString(region.Key)
		definitions.WriteString(": ")
		definitions.WriteString(region.Description)
		definitions.WriteByte('\n')
	}
	return definitions.String()
}

func AllowedRegions() map[string]struct{} {
	allowed := make(map[string]struct{}, len(regions))
	for _, region := range regions {
		allowed[region.Key] = struct{}{}
	}
	return allowed
}

func SortedRegionKeys(allowed map[string]struct{}) []string {
	keys := make([]string, 0, len(allowed))
	for key := range allowed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

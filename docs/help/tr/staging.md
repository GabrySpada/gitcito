---
title: Hazırlama
category: Değişikliklerle çalışma
order: 30
summary: Dosyanın tamamını, tek bir hunk'ı ya da tek tek satırları hazırlayın.
keywords: hazırlama staging stage unstage geri al discard hunk satır index kısmi
---

# Hazırlama

Commit panelinde üç liste vardır: **Çakışan**, **Hazırlanmamış** ve
**Hazırlanmış**. Her biri katlanabilir ve her biri onu açık mı bıraktığınızı
hatırlar.

![Hazırlanmamış bir diff ve yanındaki hunk ile dosya denetimleri](../../screenshots/line-staging.webp)

## Üç hassasiyet düzeyi

| Düzey | Nasıl |
|---|---|
| **Dosya** | Satırdaki ✚ işaretine tıklayın ya da birkaç satır seçip hepsini birden hazırlayın |
| **Hunk** | Diff'i açın ve hunk başlığındaki düğmeyi kullanın |
| **Satır** | Diff'te değişen bir satırın üzerine gelip **+** simgesine tıklayın ya da birkaç satır seçip onları stage'e alın |

Satır bazlı hazırlama, hata ayıklama amaçlı bir `console.log`'u önce silmek
zorunda kalmadan commit'in dışında tutmayı pratik hâle getiren şeydir.

## Tek tek satırları stage'e almak

Stage'e alınmamış bir dosyanın diff'ini birleşik ya da bölünmüş görünümde açın.
Eklenen veya silinen bir satırın üzerine gelin; başında küçük yeşil bir **+**
belirir: tek tıklama o satırı stage'e alır, başka hiçbir şeyi değil. Bloğun
geri kalanı stage dışında kalır — tıpkı bloğu `git add -p` içinde elle
düzenlemişsiniz gibi.

Birden çok satır için satırların kendisine tıklayarak onları seçin —
<kbd>⇧</kbd>-tıklama son tıkladığınızdan itibaren değişen tüm satırları alır — ve
diff'in üstündeki çubukta **N satırı stage’e al** düğmesine basın.

Tersi de çalışır. Bir dosyanın **stage'deki** sürümünü açın; denetimler kırmızı
bir **−**, **Bloğu stage’den çıkar** ve **N satırı stage’den çıkar** olur:
satırları index'ten geri alır, çalışma ağacına dokunmazlar.

Baktığınız taraftaki son değişikliği kaldırırsanız diff boş kalmak yerine
dosyayı diğer tarafa takip eder.

Bu şekilde stage'e aldığınız ya da çıkardığınız her satır veya blok, araç
çubuğundaki **Geri al** ile, her seferinde bir tıklamayla geri alınır.

| Seçtiğiniz | Stage'e almak | Stage'den çıkarmak |
|---|---|---|
| Eklenen bir satır | Index o satırı kazanır | Index o satırı kaybeder |
| Silinen bir satır | Index o satırı kaybeder | Satır index'e geri döner |
| İkisi de değil, aynı blokta | Olduğu gibi kalır, yalnızca çalışma ağacında | Stage'de kalır |

### Sınırlar

- **Boşluk gizliyken stage yok.** *Boşluk* yok sayılırken diff bazı değişiklikleri
  atlar ve hangi satırların stage'e alınacağını söyleyemez; denetimler siz
  kapatana kadar gizlenir.
- **İzlenmeyen dosyalar** bütün olarak stage'e alınır. Önce dosyayı stage'e alın,
  sonra istemediğiniz satırları çıkarın.
- **Sonunda satır sonu olmayan bir dosyanın son satırı**, değişikliğini ondan
  sonra eklenen satırlardan ayırırsanız reddedilebilir: git bu yarım durumu bir
  yama olarak tarif edemez. İkisini birlikte stage'e alın.
- **Geri almak için index'in bıraktığınız gibi olması gerekir.** O zamandan beri
  aynı satırlardan daha fazlasını stage'e aldıysanız git tahmin yürütmek yerine
  geri almayı reddeder ve bunu söyler.

## Değişiklikleri atma

Atma işlemi de aynı düzeylerde çalışır ve her zaman sorar. İzlenmeyen dosyalar
silinir; izlenenler hazırlanmış (ya da commit'lenmiş) hâline geri döner.

## Klavye

<kbd>↑</kbd> <kbd>↓</kbd> (ya da <kbd>j</kbd> <kbd>k</kbd>) dosya listelerinde
gezinir; aralık seçmek için <kbd>⇧</kbd>, tek tek dosyaları seçip bırakmak için
<kbd>⌘</kbd>/<kbd>Ctrl</kbd> kullanılır.

<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd> seçimi en son tıkladığınız satırdan
genişletir. Seçime sağ tıklayarak içindeki her şeyi tek seferde stage'leyin,
stage'den çıkarın, stash'leyin ya da atın.

## Yolları kopyalama

Commit'lenmemiş bir dosyaya sağ tıklayınca **Dosya yolunu kopyala** (mutlak,
platformun ayırıcılarıyla) ve **Göreli dosya yolunu kopyala** (`src/index.ts`,
başında `./` yok) çıkar. Birden fazla seçili dosya, liste sırasıyla satır başına
bir yol kopyalar. Silinmiş dosyalar etkin kalır — bu eylemler yalnızca metin
kopyalar. Klasörler hâlâ klasör yolunu kopyalar.

## Commit'lemeden önce

Gitcito birkaç şeyi denetler ve bir kez sorar, asla sessizce geçmez:

- **gizli bilgi** gibi görünen bir dosya (`.env`, `*.pem`, `id_rsa`…),
- **çok büyük** bir blob (eşik değeri Ayarlar → Güvenlik altında),
- doğrudan **korumalı bir dala commit'leme** (öntanımlı olarak `main`/`master`).

Bunların her biri tek tıkla bir *Yok say ve izlemeyi bırak* seçeneği sunar.
Bkz. [Güvenlik ve gizli bilgiler](security.md).

**Ayrıca bakınız:** [Commit'leme](committing.md) · [Diff'ler](diffs.md) · [Absorb](absorb.md)

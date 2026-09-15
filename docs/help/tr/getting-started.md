---
title: Başlarken
category: Buradan başlayın
order: 1
summary: Bir depo açın, grafiği okuyun, ilk commit'inizi alın.
keywords: giriş ilk adımlar açma klonlama sekmeler grafik commit intro open clone tabs graph
---

# Başlarken

Gitcito bir klasörü açar ve size geçmişini gösterir. Siz istemedikçe deponuza
hiçbir şey yazılmaz.

![Henüz hiç commit'i olmayan, yeni açılmış bir depo](../../screenshots/empty-repo.webp)

## Bir depo açın

- **Bir klasörü sürükleyip** pencereye bırakın ya da karşılama ekranındaki
  **Depo aç** düğmesini kullanın.
- Bir URL'den veya doğrudan barındırıcınızdan **klonlayın** — devasa bir depoyu
  hızlıca klonlamayı sağlayan seçenekler için [klonlama](cloning.md) sayfasına
  bakın.
- Terminalden `gitcito .` komutu, çalışan uygulamada geçerli klasörü açar —
  bkz. [komut satırı](cli.md).
- Henüz Git deposu olmayan bir klasör de açılır; Gitcito onu başlatmayı
  önerir.

## Üç panel

| Panel | Neler var |
|---|---|
| Sol | Dallar, uzak depolar, etiketler, stash'ler, çalışma ağaçları — ve çalışma dizini için **Dosyalar** sekmesi |
| Orta | Commit grafiği ve grafikten seçtiğiniz her şey |
| Sağ | Commit besteci paneli ya da seçili commit'in ayrıntıları |

## Geri kalan her şeyi bulmak

İki yol var ve ikisi de aynı yerlere çıkıyor:

- **`⌘K`** (`Ctrl+K`) — komut paleti. Ne istediğinizi yazın; dallara,
  commit'lere ve dosyalara da atlar.
- Araç çubuğundaki **Araçlar** — aynı depo kapsamlı küme, bu kez menü olarak;
  okunabilir kalsın diye uzun kuyruk gruplara katlanmış durumda.

![Araçlar menüsü: önce sık kullanılan araçlar, gerisi gruplanmış](../../screenshots/tools-menu.webp)

Eylem çubuğu düğmelerini **pencerenin** ortasında tutar; depo adıyla arama kutusu arasında kalan boşluğun ortasında değil. Böylece adları hiç de aynı uzunlukta olmayan depolar arasında gezinirken düğmeler aynı yerde kalır. Depo ve dal adları tam olarak görünür; çubuğun geri kalanını tehdit edecek kadar uzayan ad üç noktayla kısaltılır, tamamı da düğmenin ipucunda durur.

Yer, o orta noktadan dışa doğru ölçülür; orayı korumanın bedeli de bu: pencere daraldığında ya da depo adı çok uzun olduğunda çubuk kaymak yerine yer verir. İlk yeri arama alanı verir ve kutusunu bir büyütece bırakır — aramak için tıklayın; bir filtre etkin olduğu sürece açık kalır. Ardından artık sığmayan düğmeler, çubuktaki sırayla ve kendi alt menüleriyle birlikte çubuğun sonundaki **Daha** menüsüne katlanır. Pencereyi genişletin, geri çıkarlar.

![Dar bir pencerede eylem çubuğu: arama bir büyütece katlanmış, çubuğun sonu “Daha” menüsünde ve düğmeler hâlâ ortada](../../screenshots/toolbar-narrow.webp)

Birinden ulaşılabilen her şeye diğerinden de ulaşılır; yani yalnızca uzman
kullanıcıların bulabildiği hiçbir şey yok.

## İlk commit'iniz

1. Bir dosyayı düzenleyin. **Hazırlanmamış** başlığı altında görünecektir.
2. Hazırlayın — dosyanın tamamını, tek bir hunk'ı ya da
   [tek tek satırları](staging.md).
3. Bir mesaj yazıp **Commit**'e basın.

Gitcito'daki diğer her şey isteğe bağlıdır.


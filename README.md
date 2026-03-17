# MetricViewer

Prometheus benzeri JSONL metrik dosyalarını görselleştiren tek dosyalı web uygulaması.

## Özellikler

- **Metrik Arama**: Tüm metrikler arasında arama yaparak grafik ekleme
- **CPU Dashboard**: rate() hesaplaması ile çekirdek bazında CPU kullanım yüzdeleri
- **Memory Dashboard**: RAM kullanımı, dağılım (used/buffers/cached/free) ve swap
- **Zaman Aralığı**: 5dk, 15dk, 30dk, 1 saat (varsayılan), 3 saat, 6 saat, 12 saat, 1 gün, 1 hafta, tümü
- **Otomatik Yenileme**: 30 saniyede bir veri güncelleme
- **Tek Binary**: HTML dosyası Go binary'sine gömülüdür, ek dosya gerekmez

## Derleme

### Linux

```bash
go build -o metricviewer main.go
```

### Windows (cross-compile from Linux)

```bash
GOOS=windows GOARCH=amd64 go build -o metricviewer.exe main.go
```

### Windows (Windows üzerinde)

```powershell
go build -o metricviewer.exe main.go
```

## Kullanım

### Linux

```bash
./metricviewer --db /path/to/metrics.db --listen :9099
```

### Windows (PowerShell)

```powershell
# Varsayılan ayarlarla çalıştırma (metrics.db, port 9099)
.\metricviewer.exe

# Özel dosya yolu ve port
.\metricviewer.exe --db C:\Users\admin\metrics.db --listen :8080

# Belirli IP'de yayınlama
.\metricviewer.exe --db .\data\metrics.db --listen 192.168.1.100:9099
```

### Parametreler

| Parametre | Varsayılan | Açıklama |
|-----------|-----------|----------|
| `--db`    | `metrics.db` | JSONL formatındaki metrik dosyasının yolu |
| `--listen`| `:9099`    | Web sunucusunun dinleyeceği adres ve port |

## Tarayıcıda Açma

Uygulama başlatıldıktan sonra tarayıcınızda açın:

```
http://localhost:9099
```

## metrics.db Dosya Formatı

Her satır bir JSON kaydıdır (JSONL):

```json
{"timestamp":"2026-03-17T05:19:51Z","ts_unix":1773724791.85,"name":"node_cpu_seconds_total","labels":"cpu=0,mode=user","value":812.17,"type":"counter"}
```

| Alan | Açıklama |
|------|----------|
| `timestamp` | ISO 8601 zaman damgası |
| `ts_unix` | Unix epoch (saniye, ondalıklı) |
| `name` | Metrik adı |
| `labels` | Etiketler (`key=val,key=val` formatı) |
| `value` | Sayısal değer |
| `type` | Metrik tipi (counter, gauge, vb.) |

## API Endpointleri

| Endpoint | Açıklama |
|----------|----------|
| `GET /` | Web arayüzü |
| `GET /api/metrics` | Tüm metrik adları |
| `GET /api/labels?name=X` | Belirli metriğin etiket setleri |
| `GET /api/query?name=X&labels=Y` | Zaman serisi verisi |
| `GET /api/timerange` | Veri setindeki min/max zaman |
| `GET /api/cpu` | CPU kullanım yüzdeleri (rate hesaplaması) |
| `GET /api/memory` | Bellek kullanım bilgisi |

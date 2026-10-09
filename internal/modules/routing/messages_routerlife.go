package routing

import "github.com/tikhonp/proxier/internal/platform/i18n"

// routerLifeMessages are the texts of drift, unmanaged tags, awaiting setup,
// pause, removal and the dashboard's Routing area (3f).
var routerLifeMessages = i18n.Messages{
	// The routers list
	"routers.col.watch":       {EN: "Watch", RU: "Внимание"},
	"routers.watch.drift":     {EN: "▲ drift: {tags}", RU: "▲ расхождения: {tags}"},
	"routers.watch.unmanaged": {EN: "▲ {n} unmanaged tag: {tags}|▲ {n} unmanaged tags: {tags}", RU: "▲ {n} чужой тег: {tags}|▲ {n} чужих тега: {tags}|▲ {n} чужих тегов: {tags}"},
	"routers.watch.none":      {EN: "none", RU: "нет"},
	"routers.awaiting.trying": {EN: "never connected · trying every 10 min until {date}", RU: "ещё не подключался · попытки каждые 10 мин до {date}"},
	"routers.awaiting.never":  {EN: "never connected", RU: "так и не подключился"},
	"routers.band.removed":    {EN: "Removed {name}.", RU: "Роутер {name} удалён."},

	// Bands
	"routers.band.awaiting":        {EN: "Awaiting setup: Proxier tries to connect every 10 minutes until {date}. Install Proxier's key on the router.", RU: "Ждёт настройки: Proxier пытается подключиться каждые 10 минут до {date}. Установите ключ Proxier на роутер."},
	"routers.band.awaiting_over":   {EN: "Never connected: Proxier stopped trying on {date}. Install Proxier's key on the router, then Test connection.", RU: "Так и не подключился: Proxier перестал пытаться {date}. Установите ключ Proxier на роутер и проверьте подключение."},
	"routers.band.probe_error":     {EN: "Last try at {time}: {error}", RU: "Последняя попытка в {time}: {error}"},
	"routers.band.paused":          {EN: "Paused: changes wait until you resume.", RU: "На паузе: изменения ждут, пока вы не снимете паузу."},
	"routers.band.removing":        {EN: "Removing every tag Proxier installed from {name}.", RU: "Удаление с {name} всех тегов, которые поставил Proxier."},
	"routers.band.removing_failed": {EN: "The removal failed. Retry it, or remove the router without cleaning.", RU: "Удаление не удалось. Повторите его или удалите роутер без очистки."},
	"routers.band.removing_job":    {EN: "Open the removal job", RU: "Открыть задачу удаления"},
	"routers.band.drift":           {EN: "Drift found at {time}: {what}.", RU: "Расхождения найдены в {time}: {what}."},
	"routers.band.drift_repairs":   {EN: "A repair sync runs on its own.", RU: "Исправляющая синхронизация запускается сама."},
	"routers.band.repairing":       {EN: "Repair queued: a sync runs now.", RU: "Исправление поставлено в очередь: синхронизация начнётся сейчас."},
	"routers.band.adopted":         {EN: "Adopted {tag}: a sync runs now and records it.", RU: "Тег {tag} принят: синхронизация начнётся сейчас и запишет его."},
	"routers.drift":                {EN: "Drift", RU: "Расхождения"},
	"routers.drift.fewer":          {EN: "{tag} has {n} entry fewer than Proxier installed|{tag} has {n} entries fewer than Proxier installed", RU: "у {tag} на {n} запись меньше, чем поставил Proxier|у {tag} на {n} записи меньше, чем поставил Proxier|у {tag} на {n} записей меньше, чем поставил Proxier"},
	"routers.drift.more":           {EN: "{tag} has {n} entry more than Proxier installed|{tag} has {n} entries more than Proxier installed", RU: "у {tag} на {n} запись больше, чем поставил Proxier|у {tag} на {n} записи больше, чем поставил Proxier|у {tag} на {n} записей больше, чем поставил Proxier"},
	"routers.drift.differs":        {EN: "{tag} differs from what Proxier installed", RU: "{tag} отличается от того, что поставил Proxier"},
	"routers.repair":               {EN: "Repair", RU: "Исправить"},
	"routers.conn.drift":           {EN: "Drift check", RU: "Проверка расхождений"},
	"routers.conn.drift.on":        {EN: "every {every} · auto-repair on", RU: "каждые {every} · автоисправление включено"},
	"routers.conn.drift.off":       {EN: "every {every} · auto-repair off", RU: "каждые {every} · автоисправление выключено"},

	// Per-hop table
	"routers.hops":            {EN: "Per hop", RU: "По участкам"},
	"routers.hop.tailnet":     {EN: "tailnet node {name}", RU: "узел tailnet {name}"},
	"routers.hop.jump":        {EN: "jump host {name}", RU: "промежуточный хост {name}"},
	"routers.hop.router":      {EN: "router {name}", RU: "роутер {name}"},
	"routers.hop.up":          {EN: "up", RU: "работает"},
	"routers.hop.up_tailnet":  {EN: "up · Proxier itself is on the tailnet", RU: "работает · сам Proxier в tailnet"},
	"routers.hop.off":         {EN: "off", RU: "выключен"},
	"routers.hop.offline":     {EN: "offline", RU: "недоступен"},
	"routers.hop.refused":     {EN: "refused Proxier's key", RU: "отверг ключ Proxier"},
	"routers.hop.key-changed": {EN: "host key changed", RU: "ключ хоста изменился"},
	"routers.hop.unknown":     {EN: "key not confirmed yet", RU: "ключ ещё не подтверждён"},
	"routers.hop.not-tried":   {EN: "not tried", RU: "не проверялся"},
	"routers.hop.via":         {EN: "not tried · reached only through {via}", RU: "не проверялся · доступен только через {via}"},

	// Not Proxier's
	"routers.area.unmanaged":         {EN: "Not Proxier's", RU: "Не от Proxier"},
	"routers.area.unmanaged.note":    {EN: "found on the router · never deleted on its own", RU: "найдено на роутере · само не удаляется"},
	"routers.unmanaged.entries":      {EN: "{n} entry|{n} entries", RU: "{n} запись|{n} записи|{n} записей"},
	"routers.unmanaged.since":        {EN: "seen since {date}", RU: "видно с {date}"},
	"routers.unmanaged.catalog":      {EN: "the catalog has {selector}", RU: "в каталоге есть {selector}"},
	"routers.unmanaged.existing":     {EN: "a service {tag} exists", RU: "сервис {tag} уже есть"},
	"routers.unmanaged.invalid":      {EN: "Not a valid tag: it can only be removed or ignored.", RU: "Недопустимый тег: его можно только удалить или игнорировать."},
	"routers.unmanaged.adopt":        {EN: "Adopt…", RU: "Принять…"},
	"routers.unmanaged.ignore":       {EN: "Ignore", RU: "Игнорировать"},
	"routers.unmanaged.unignore":     {EN: "Stop ignoring", RU: "Не игнорировать"},
	"routers.unmanaged.remove":       {EN: "Remove from router…", RU: "Удалить с роутера…"},
	"routers.unmanaged.remove.title": {EN: "Remove {tag} ({n} entry) from {router}?|Remove {tag} ({n} entries) from {router}?", RU: "Удалить {tag} ({n} запись) с {router}?|Удалить {tag} ({n} записи) с {router}?|Удалить {tag} ({n} записей) с {router}?"},
	"routers.unmanaged.remove.body":  {EN: "Proxier never installed it; it goes on the next sync, now.", RU: "Его ставил не Proxier; он будет удалён следующей синхронизацией, сейчас."},
	"routers.unmanaged.ignored":      {EN: "Ignored · {n}", RU: "Игнорируются · {n}"},
	"routers.untagged":               {EN: "{n} entry without a comment. Left alone unless a desired tag includes its name.|{n} entries without a comment. Left alone unless a desired tag includes their name.", RU: "{n} запись без комментария. Не трогается, если её имя не входит в нужный тег.|{n} записи без комментария. Не трогаются, если их имя не входит в нужный тег.|{n} записей без комментария. Не трогаются, если их имя не входит в нужный тег."},
	"routers.infra":                  {EN: "{n} entry commented mtvpn: belongs to the router script. Never touched.|{n} entries commented mtvpn: belong to the router script. Never touched.", RU: "{n} запись с комментарием mtvpn: относится к скрипту роутера. Не трогается.|{n} записи с комментарием mtvpn: относятся к скрипту роутера. Не трогаются.|{n} записей с комментарием mtvpn: относятся к скрипту роутера. Не трогаются."},

	// Adopt
	"routers.adopt":               {EN: "Adopt", RU: "Принять"},
	"routers.adopt.title":         {EN: "Adopt {tag}", RU: "Принять {tag}"},
	"routers.adopt.intro":         {EN: "{tag} has {n} entry on {router} that Proxier never installed. Adopting it adds a service with that tag to {list}; the next sync records or updates it.|{tag} has {n} entries on {router} that Proxier never installed. Adopting it adds a service with that tag to {list}; the next sync records or updates it.", RU: "У {tag} на {router} {n} запись, которую ставил не Proxier. Если принять тег, сервис с ним добавится в {list}, а следующая синхронизация запишет или обновит его.|У {tag} на {router} {n} записи, которые ставил не Proxier. Если принять тег, сервис с ним добавится в {list}, а следующая синхронизация запишет или обновит его.|У {tag} на {router} {n} записей, которые ставил не Proxier. Если принять тег, сервис с ним добавится в {list}, а следующая синхронизация запишет или обновит его."},
	"routers.adopt.invalid":       {EN: "“{tag}” can't be a service's tag: remove it from the router or ignore it.", RU: "«{tag}» не может быть тегом сервиса: удалите его с роутера или игнорируйте."},
	"routers.adopt.as":            {EN: "Adopt as {selector}", RU: "Принять как {selector}"},
	"routers.adopt.as.what":       {EN: "The catalog's list, added to {list} and refreshed daily.", RU: "Список из каталога, добавляется в {list} и обновляется ежедневно."},
	"routers.adopt.existing":      {EN: "Add {tag} to {list}", RU: "Добавить {tag} в {list}"},
	"routers.adopt.existing.what": {EN: "The service with this tag already exists.", RU: "Сервис с этим тегом уже есть."},
	"routers.adopt.custom":        {EN: "Make a custom service from the router's {n} entry|Make a custom service from the router's {n} entries", RU: "Создать свой сервис из {n} записи роутера|Создать свой сервис из {n} записей роутера|Создать свой сервис из {n} записей роутера"},
	"routers.adopt.custom.what":   {EN: "Named and tagged {tag}; you edit its names in Proxier from now on.", RU: "С именем и тегом {tag}; дальше его имена правятся в Proxier."},
	"routers.adopt_remove":        {EN: "Remove from router…", RU: "Удалить с роутера…"},
	"routers.adopt_ignore":        {EN: "Ignore", RU: "Игнорировать"},
	"routers.adopt_v2fly":         {EN: "Adopt as v2fly", RU: "Принять как v2fly"},
	"routers.adopt_iplist":        {EN: "Adopt as iplist", RU: "Принять как iplist"},
	"routers.adopt_existing":      {EN: "Add the existing service", RU: "Добавить существующий сервис"},
	"routers.adopt_custom":        {EN: "Make a custom service", RU: "Создать свой сервис"},

	// Pause, remove
	"routers.pause":             {EN: "Pause syncs", RU: "Приостановить"},
	"routers.pause_row":         {EN: "Pause syncs", RU: "Приостановить"},
	"routers.pause.what":        {EN: "Changes wait until you resume; no drift checks.", RU: "Изменения ждут снятия паузы; расхождения не проверяются."},
	"routers.resume":            {EN: "Resume", RU: "Возобновить"},
	"routers.resume_row":        {EN: "Resume", RU: "Возобновить"},
	"routers.resume.what":       {EN: "Runs a full sync now.", RU: "Сразу запускает полную синхронизацию."},
	"routers.remove":            {EN: "Remove…", RU: "Удалить…"},
	"routers.remove.what":       {EN: "Remove the router from Proxier, keeping or removing what Proxier installed.", RU: "Удалить роутер из Proxier, оставив или убрав то, что поставил Proxier."},
	"routers.remove.title":      {EN: "Remove {name}", RU: "Удалить {name}"},
	"routers.remove.intro":      {EN: "What happens to what Proxier installed on {name}?", RU: "Что сделать с тем, что Proxier поставил на {name}?"},
	"routers.remove.keep":       {EN: "Keep everything on the router", RU: "Оставить всё на роутере"},
	"routers.remove.keep.what":  {EN: "The router is removed from Proxier at once; its DNS entries and address list stay as they are.", RU: "Роутер сразу удаляется из Proxier; его записи DNS и адресный список остаются как есть."},
	"routers.remove.clean":      {EN: "Remove every tag Proxier installed", RU: "Убрать все теги, которые поставил Proxier"},
	"routers.remove.clean.what": {EN: "A removal sync deletes the {n} tag Proxier installed (infra pins, untagged entries and tags Proxier didn't install stay), then the router is removed.|A removal sync deletes the {n} tags Proxier installed (infra pins, untagged entries and tags Proxier didn't install stay), then the router is removed.", RU: "Синхронизация удалит {n} тег, который поставил Proxier (служебные записи, записи без тега и чужие теги остаются), затем роутер удаляется.|Синхронизация удалит {n} тега, которые поставил Proxier (служебные записи, записи без тега и чужие теги остаются), затем роутер удаляется.|Синхронизация удалит {n} тегов, которые поставил Proxier (служебные записи, записи без тега и чужие теги остаются), затем роутер удаляется."},
	"routers.remove.do":         {EN: "Remove", RU: "Удалить"},
	"routers.remove_confirm":    {EN: "Remove the router", RU: "Удалить роутер"},
	"routers.remove.retry":      {EN: "Retry", RU: "Повторить"},
	"routers.remove_retry":      {EN: "Retry the removal", RU: "Повторить удаление"},
	"routers.remove.keep_now":   {EN: "Remove without cleaning", RU: "Удалить без очистки"},
	"routers.remove_keep":       {EN: "Remove without cleaning", RU: "Удалить без очистки"},

	// Dashboard
	"routers.dash.title":          {EN: "Routing", RU: "Маршрутизация"},
	"routers.dash.all":            {EN: "Routers →", RU: "Роутеры →"},
	"routers.dash.failed":         {EN: "sync failed at {step} · {ago}", RU: "синхронизация не удалась на шаге {step} · {ago}"},
	"routers.dash.in_sync":        {EN: "{n} router in sync|{n} routers in sync", RU: "{n} роутер синхронизирован|{n} роутера синхронизированы|{n} роутеров синхронизированы"},
	"routers.dash.snapshots":      {EN: "Snapshots", RU: "Снимки"},
	"routers.dash.snapshots.none": {EN: "None waiting for a decision.", RU: "Решения не ждёт ни один."},
	"routers.dash.drop":           {EN: "{pct} % drop", RU: "минус {pct} %"},
	"routers.dash.review":         {EN: "Review", RU: "Посмотреть"},
	"routers.dash.refresh":        {EN: "Daily refresh", RU: "Ежедневное обновление"},
	"routers.dash.refresh.none":   {EN: "not run yet", RU: "ещё не запускалось"},
	"routers.dash.digest":         {EN: "{time} · {n} service changed (+{added} / −{removed} domains) · {rejected} rejected|{time} · {n} services changed (+{added} / −{removed} domains) · {rejected} rejected", RU: "{time} · изменился {n} сервис (+{added} / −{removed} доменов) · отклонено {rejected}|{time} · изменились {n} сервиса (+{added} / −{removed} доменов) · отклонено {rejected}|{time} · изменились {n} сервисов (+{added} / −{removed} доменов) · отклонено {rejected}"},
}

func init() {
	for k, v := range routerLifeMessages {
		messages[k] = v
	}
}

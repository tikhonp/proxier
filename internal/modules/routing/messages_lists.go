package routing

import "github.com/tikhonp/proxier/internal/platform/i18n"

// listMessages are the routing lists' texts (3b).
var listMessages = i18n.Messages{
	// Nav, the lists page, search
	"lists.nav":                 {EN: "Lists", RU: "Списки"},
	"lists.title":               {EN: "Routing lists", RU: "Списки маршрутизации"},
	"lists.intro":               {EN: "A list is an ordered set of services. Each router and each Shadowrocket config follows one list and sends its domains through the tunnel.", RU: "Список — упорядоченный набор сервисов. Каждый роутер и каждый конфиг Shadowrocket следует одному списку и отправляет его домены в туннель."},
	"lists.status":              {EN: "{n} list|{n} lists", RU: "{n} список|{n} списка|{n} списков"},
	"lists.search":              {EN: "routing list · {n} service|routing list · {n} services", RU: "список маршрутизации · {n} сервис|список маршрутизации · {n} сервиса|список маршрутизации · {n} сервисов"},
	"lists.new":                 {EN: "New list", RU: "Новый список"},
	"lists.col.list":            {EN: "List", RU: "Список"},
	"lists.col.services":        {EN: "Services", RU: "Сервисы"},
	"lists.col.domains":         {EN: "Domains", RU: "Домены"},
	"lists.col.targets":         {EN: "Targets", RU: "Цели"},
	"lists.col.warnings":        {EN: "Warnings", RU: "Предупреждения"},
	"lists.default_line":        {EN: "default for new targets", RU: "по умолчанию для новых целей"},
	"lists.warnings_n":          {EN: "{n} warning|{n} warnings", RU: "{n} предупреждение|{n} предупреждения|{n} предупреждений"},
	"lists.none":                {EN: "none", RU: "нет"},
	"lists.hints.row":           {EN: "↵ open {name}", RU: "↵ открыть {name}"},
	"lists.target.router":       {EN: "router", RU: "роутер"},
	"lists.target.shadowrocket": {EN: "Shadowrocket", RU: "Shadowrocket"},

	// New list, rename
	"lists.new.title":         {EN: "New routing list", RU: "Новый список маршрутизации"},
	"lists.edit.title":        {EN: "Rename {name}", RU: "Переименовать {name}"},
	"lists.field.name":        {EN: "Name", RU: "Название"},
	"lists.field.description": {EN: "Description", RU: "Описание"},
	"lists.create":            {EN: "Create list", RU: "Создать список"},
	"lists.save":              {EN: "Save", RU: "Сохранить"},
	"lists.err.name":          {EN: "Enter a name of 1 to 60 characters.", RU: "Введите название от 1 до 60 символов."},
	"lists.err.name_taken":    {EN: "Another list has this name.", RU: "Это название уже занято другим списком."},
	"lists.err.description":   {EN: "At most 1 000 characters.", RU: "Не больше 1 000 символов."},
	"lists.err.gone":          {EN: "That routing list was deleted meanwhile.", RU: "Тем временем этот список удалили."},
	"lists.err.guard":         {EN: "Adding {domain} would cover {hostname}, the hostname of {server}. It wasn't saved.", RU: "Добавление {domain} охватило бы {hostname} — имя хоста сервера {server}. Ничего не сохранено."},

	// The list page
	"lists.services_n":         {EN: "{n} service|{n} services", RU: "{n} сервис|{n} сервиса|{n} сервисов"},
	"lists.domains_n":          {EN: "{n} domain|{n} domains", RU: "{n} домен|{n} домена|{n} доменов"},
	"lists.added_band":         {EN: "Added {n} service.|Added {n} services.", RU: "Добавлен {n} сервис.|Добавлено {n} сервиса.|Добавлено {n} сервисов."},
	"lists.area.services":      {EN: "Services, in order", RU: "Сервисы по порядку"},
	"lists.area.services.note": {EN: "a domain belongs to the first service that has it", RU: "домен принадлежит первому сервису, в котором он есть"},
	"lists.services.note":      {EN: "Drag, or J K on the selected row, to reorder. Targets get the new order on their next sync.", RU: "Перетащите или нажмите J K на выбранной строке, чтобы изменить порядок. Цели получат его при следующей синхронизации."},
	"lists.services.empty":     {EN: "No services yet. Targets following {name} get nothing from Proxier.", RU: "Сервисов пока нет. Цели, следующие списку {name}, ничего не получают от Proxier."},
	"lists.drag":               {EN: "Drag to reorder", RU: "Перетащите, чтобы изменить порядок"},
	"lists.owned_total":        {EN: "installed here / in its snapshot", RU: "устанавливается здесь / в снимке"},
	"lists.hints.member":       {EN: "J K move {tag} down · up", RU: "J K сдвинуть {tag} вниз · вверх"},
	"lists.move.up":            {EN: "Move {tag} up", RU: "Поднять {tag}"},
	"lists.move.down":          {EN: "Move {tag} down", RU: "Опустить {tag}"},
	"lists.remove":             {EN: "×", RU: "×"},
	"lists.remove.title":       {EN: "Take {tag} out of {name}?", RU: "Убрать {tag} из списка {name}?"},
	"lists.remove.body":        {EN: "Its names leave the targets of {name} on their next sync, unless another service in {name} has them.", RU: "Его имена уйдут с целей списка {name} при следующей синхронизации, если их нет у другого сервиса в {name}."},
	"lists.order.stale":        {EN: "The services changed meanwhile: this is the order as it is now.", RU: "Тем временем сервисы изменились: вот текущий порядок."},
	"lists.undo":               {EN: "Undo", RU: "Отменить"},
	"lists.undo.above":         {EN: "Moved {tag} above {other}", RU: "{tag} теперь выше {other}"},
	"lists.undo.below":         {EN: "Moved {tag} below {other}", RU: "{tag} теперь ниже {other}"},
	"lists.undo.reordered":     {EN: "Reordered the services", RU: "Порядок сервисов изменён"},
	"lists.undo.no_owner":      {EN: "No domain changes owner.", RU: "Ни один домен не сменил владельца."},
	"lists.undo.owners":        {EN: "{n} domain changes owner ({names}).|{n} domains change owner ({names}).", RU: "{n} домен сменил владельца ({names}).|{n} домена сменили владельца ({names}).|{n} доменов сменили владельца ({names})."},
	"lists.undo.targets":       {EN: "{targets} re-sync.", RU: "{targets} синхронизируются заново."},
	"lists.area.targets":       {EN: "Targets · {n}", RU: "Цели · {n}"},
	"lists.targets.none":       {EN: "No routers or Shadowrocket configs follow {name} yet.", RU: "Пока ни один роутер и ни один конфиг Shadowrocket не следует списку {name}."},
	"lists.area.warnings":      {EN: "Warnings · {n}", RU: "Предупреждения · {n}"},
	"lists.warnings.none":      {EN: "Nothing to look at.", RU: "Всё в порядке."},
	"lists.warning.refused":    {EN: "Refused {domain} from {service}: it covers {hostname}, the hostname of {server}.", RU: "{domain} из {service} не принят: он охватывает {hostname} — имя хоста сервера {server}."},
	"lists.warning.guarded":    {EN: "{source} has {domain}, which covers {hostname} ({server}): left out of what targets get.", RU: "В {source} есть {domain}, который охватывает {hostname} ({server}): не передаётся целям."},
	"lists.area.actions":       {EN: "Actions", RU: "Действия"},
	"lists.area.activity":      {EN: "Activity", RU: "Журнал"},
	"lists.rename":             {EN: "Rename…", RU: "Переименовать…"},
	"lists.rename.note":        {EN: "Name and description; targets don't change.", RU: "Название и описание; цели не меняются."},
	"lists.make_default":       {EN: "Make default", RU: "Сделать основным"},
	"lists.make_default.note":  {EN: "New routers and Shadowrocket configs follow it.", RU: "Новые роутеры и конфиги Shadowrocket будут следовать ему."},
	"lists.delete":             {EN: "Delete…", RU: "Удалить…"},
	"lists.delete.note":        {EN: "Its services stay; its targets move to another list.", RU: "Сервисы останутся; цели перейдут на другой список."},
	"lists.event.added":        {EN: "Added {added}", RU: "Добавлено: {added}"},
	"lists.event.removed":      {EN: "Took {removed} out", RU: "Убрано: {removed}"},
	"lists.event.reordered":    {EN: "Reordered the services", RU: "Изменён порядок сервисов"},
	"lists.event.default":      {EN: "Made {subject} the default", RU: "{subject} стал основным"},
	"lists.event.fields":       {EN: "Changed the {changes}", RU: "Изменено: {changes}"},

	// Add services
	"lists.add":             {EN: "Add services…", RU: "Добавить сервисы…"},
	"lists.add.empty":       {EN: "Add services…", RU: "Добавить сервисы…"},
	"lists.add.title":       {EN: "Add services to {name}", RU: "Добавить сервисы в {name}"},
	"lists.add.note":        {EN: "They go to the end of the list, in tag order.", RU: "Они встанут в конец списка, по алфавиту тегов."},
	"lists.add.filter":      {EN: "Filter by tag or selector", RU: "Фильтр по тегу или селектору"},
	"lists.add.none":        {EN: "Every service is in this list already.", RU: "Все сервисы уже в этом списке."},
	"lists.add.domains":     {EN: "{n} domains", RU: "доменов: {n}"},
	"lists.add.submit":      {EN: "Add the ticked services", RU: "Добавить отмеченные сервисы"},
	"lists.add.new":         {EN: "Or add a new one", RU: "Или добавьте новый"},
	"lists.add.new_submit":  {EN: "Add service to {name}", RU: "Добавить сервис в {name}"},
	"lists.add_to":          {EN: "Add to routing lists", RU: "Добавить в списки маршрутизации"},
	"lists.add_to.help":     {EN: "It goes to the end of each ticked list. Untick all to add it to no list.", RU: "Он встанет в конец каждого отмеченного списка. Снимите все отметки, чтобы не добавлять его никуда."},
	"lists.to_lists":        {EN: "Add to lists…", RU: "Добавить в списки…"},
	"lists.to_lists.title":  {EN: "Add {tag} to lists", RU: "Добавить {tag} в списки"},
	"lists.to_lists.note":   {EN: "It goes to the end of each list.", RU: "Он встанет в конец каждого списка."},
	"lists.to_lists.none":   {EN: "Every list holds it already.", RU: "Он уже есть во всех списках."},
	"lists.to_lists.submit": {EN: "Add to the ticked lists", RU: "Добавить в отмеченные списки"},

	// Delete
	"lists.delete.title":             {EN: "Delete {name}", RU: "Удалить {name}"},
	"lists.delete.default":           {EN: "The default list can't be deleted. Make another list the default first.", RU: "Основной список нельзя удалить. Сначала сделайте основным другой список."},
	"lists.delete.has_targets":       {EN: "{n} target follows {name}. Move it to another list in the same step.|{n} targets follow {name}. Move them to another list in the same step.", RU: "Списку {name} следует {n} цель. Перенесите её на другой список.|Списку {name} следуют {n} цели. Перенесите их на другой список.|Списку {name} следуют {n} целей. Перенесите их на другой список."},
	"lists.delete.move_to_field":     {EN: "Move them to", RU: "Перенести на"},
	"lists.delete.move":              {EN: "Move and delete", RU: "Перенести и удалить"},
	"lists.delete.move_submit":       {EN: "Move and delete {name}", RU: "Перенести и удалить {name}"},
	"lists.delete.move_to":           {EN: "Choose another list for its targets.", RU: "Выберите другой список для его целей."},
	"lists.delete.targets_meanwhile": {EN: "A target started following this list meanwhile.", RU: "Тем временем этому списку стала следовать цель."},
	"lists.delete.body":              {EN: "Delete {name}? Its {n} service stays; only the list goes.|Delete {name}? Its {n} services stay; only the list goes.", RU: "Удалить {name}? Его {n} сервис останется, удалится только список.|Удалить {name}? Его {n} сервиса останутся, удалится только список.|Удалить {name}? Его {n} сервисов останутся, удалится только список."},
	"lists.delete.submit":            {EN: "Delete {name}", RU: "Удалить {name}"},

	// Ownership on the service page and in the editor
	"lists.dropped_in":     {EN: "Not installed in {list}: {n} name|Not installed in {list}: {n} names", RU: "Не устанавливается в {list}: {n} имя|Не устанавливается в {list}: {n} имени|Не устанавливается в {list}: {n} имён"},
	"lists.drop.covered":   {EN: "covered by {via} ({by})", RU: "охвачен {via} ({by})"},
	"lists.drop.owned":     {EN: "owned by {by} (first in {list})", RU: "принадлежит {by} (выше в {list})"},
	"lists.drop.guarded":   {EN: "left out: covers {via} ({by})", RU: "не передаётся: охватывает {via} ({by})"},
	"lists.drop.pinned":    {EN: "left out: {via}", RU: "не передаётся: {via}"},
	"lists.editor.covered": {EN: "covered by {tag} in {list}", RU: "охвачен {tag} в списке {list}"},
	"lists.editor.footer":  {EN: "Saving changes what the targets of {lists} get.", RU: "Сохранение изменит то, что получают цели списков {lists}."},

	// The services list's filters
	"lists.filter.list": {EN: "in {name}", RU: "в {name}"},
	"lists.filter.none": {EN: "Not in any list · {n}", RU: "Ни в одном списке · {n}"},
}

func init() {
	for k, v := range listMessages {
		messages[k] = v
	}
}

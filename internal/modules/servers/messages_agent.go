package servers

import "github.com/tikhonp/proxier/internal/platform/i18n"

// agentMessages are the texts of the agent hand-off (1h). What the agent itself
// reads (the prompt, the API's answers) is English and lives in servers/agent.
var agentMessages = i18n.Messages{
	"job.servers.agent_sessions":             {EN: "Close expired agent sessions", RU: "Закрыть истёкшие сеансы агентов"},
	"job.servers.agent_sessions.step.expire": {EN: "Close expired sessions", RU: "Закрыть истёкшие сеансы"},

	"agent.handoff":       {EN: "Hand off to an agent…", RU: "Передать агенту…"},
	"agent.handoff.short": {EN: "Hand off", RU: "Передача агенту"},
	"agent.title":         {EN: "Hand off the draft to an agent", RU: "Передать черновик агенту"},
	"agent.based_on":      {EN: "draft based on v{v}", RU: "черновик на основе v{v}"},
	"agent.no_base":       {EN: "a draft that was never published", RU: "черновик, который не публиковался"},
	"agent.for_whom":      {EN: "for Claude Code, Codex or any agent that can make HTTP requests", RU: "для Claude Code, Codex или любого агента, который умеет делать HTTP-запросы"},

	"agent.problem":      {EN: "What's the problem, and what should it do?", RU: "В чём проблема и что агенту сделать?"},
	"agent.problem.help": {EN: "The agent gets this text as it is. Say what to keep as well as what to change.", RU: "Агент получит этот текст как есть. Напишите и что менять, и что оставить."},
	"agent.context":      {EN: "Context it can read", RU: "Что он может прочитать"},

	"agent.ctx.report":       {EN: "Validation report", RU: "Отчёт проверки"},
	"agent.ctx.report.note":  {EN: "computed when you create the hand-off", RU: "считается при создании передачи"},
	"agent.ctx.job":          {EN: "Failed job log", RU: "Журнал упавшей задачи"},
	"agent.ctx.job.none":     {EN: "None", RU: "Нет"},
	"agent.ctx.job.item":     {EN: "{server} · job #{id} · step {step}", RU: "{server} · задача #{id} · шаг {step}"},
	"agent.ctx.job.help":     {EN: "Jobs of servers built from this template. The log is the stored one, with secrets already hidden.", RU: "Задачи серверов, построенных из этого шаблона. Журнал — сохранённый, секреты в нём уже скрыты."},
	"agent.ctx.preview":      {EN: "Preview for {name}", RU: "Предпросмотр для {name}"},
	"agent.ctx.preview.note": {EN: "rendered files, secrets masked", RU: "готовые файлы, секреты скрыты"},
	"agent.ctx.diff":         {EN: "Diff against the base version", RU: "Отличия от базовой версии"},
	"agent.ctx.diff.note":    {EN: "what the draft already changed", RU: "что в черновике уже изменено"},
	"agent.ctx.always":       {EN: "The manifest reference and the validator rules are always included.", RU: "Справочник манифеста и правила проверки доступны всегда."},

	"agent.agent":             {EN: "Agent", RU: "Агент"},
	"agent.agent.claude-code": {EN: "Claude Code", RU: "Claude Code"},
	"agent.agent.codex":       {EN: "Codex", RU: "Codex"},
	"agent.agent.other":       {EN: "Other", RU: "Другой"},
	"agent.agent.note":        {EN: "Changes only the wording. The API is the same.", RU: "Меняет только формулировку. API одно и то же."},
	"agent.for":               {EN: "Access expires after", RU: "Доступ закончится через"},
	"agent.for.30":            {EN: "30 min", RU: "30 мин"},
	"agent.for.120":           {EN: "2 h", RU: "2 ч"},
	"agent.for.480":           {EN: "8 h", RU: "8 ч"},

	"agent.can":    {EN: "It can", RU: "Он может"},
	"agent.can.1":  {EN: "read and write this draft", RU: "читать и менять этот черновик"},
	"agent.can.2":  {EN: "validate it", RU: "проверять его"},
	"agent.can.3":  {EN: "preview it for the servers you tick", RU: "смотреть его для отмеченных серверов"},
	"agent.can.4":  {EN: "read the context above", RU: "читать контекст выше"},
	"agent.cant":   {EN: "It can't", RU: "Он не может"},
	"agent.cant.1": {EN: "publish or discard", RU: "публиковать или отбрасывать"},
	"agent.cant.2": {EN: "deploy to any server", RU: "выкатывать на серверы"},
	"agent.cant.3": {EN: "see a secret value", RU: "видеть секретные значения"},
	"agent.cant.4": {EN: "touch anything else", RU: "трогать что-либо ещё"},
	"agent.create": {EN: "Create the hand-off", RU: "Создать передачу"},

	"agent.err.problem": {EN: "Say what the agent should do (up to 4000 characters).", RU: "Напишите, что агенту сделать (до 4000 символов)."},
	"agent.err.agent":   {EN: "Choose an agent.", RU: "Выберите агента."},
	"agent.err.job":     {EN: "That job does not belong to a server built from this template.", RU: "Эта задача не относится к серверу из этого шаблона."},
	"agent.err.server":  {EN: "That server is not an active server of this template.", RU: "Этот сервер не активен или построен из другого шаблона."},

	"agent.prompt":            {EN: "Prompt", RU: "Подсказка"},
	"agent.prompt.note":       {EN: "Markdown · {size} · the token is shown once", RU: "Markdown · {size} · токен показывается один раз"},
	"agent.secret.warn":       {EN: "The prompt holds the token in plain text. Anyone who has it can edit this draft until the access ends — expires", RU: "В подсказке токен открытым текстом. Любой, у кого она есть, может править этот черновик, пока доступ не закончится — истекает"},
	"agent.copy":              {EN: "Copy prompt", RU: "Скопировать подсказку"},
	"agent.copy.then":         {EN: "then paste it into Claude Code or Codex", RU: "и вставьте её в Claude Code или Codex"},
	"agent.to_editor":         {EN: "Open the editor", RU: "Открыть редактор"},
	"agent.to_template":       {EN: "Back to the template", RU: "К шаблону"},
	"agent.repeated.title":    {EN: "Already copied", RU: "Уже скопировано"},
	"agent.repeated.body":     {EN: "The prompt was shown once and is not kept. Open a new hand-off to get another; it replaces the first.", RU: "Подсказка показывается один раз и нигде не хранится. Создайте новую передачу — она заменит первую."},
	"agent.repeated.again":    {EN: "New hand-off", RU: "Новая передача"},
	"agent.band.title":        {EN: "Handed to {agent}", RU: "Передано: {agent}"},
	"agent.band.counts":       {EN: "{saves} saves · {validations} validations", RU: "сохранений: {saves} · проверок: {validations}"},
	"agent.band.expires":      {EN: "expires", RU: "истекает"},
	"agent.band.changes":      {EN: "What it changed", RU: "Что он изменил"},
	"agent.band.revoke":       {EN: "Revoke access", RU: "Отозвать доступ"},
	"agent.revoke.title":      {EN: "Revoke the agent's access?", RU: "Отозвать доступ агента?"},
	"agent.revoke.body":       {EN: "The token stops working at once. What the agent saved stays in the draft.", RU: "Токен перестанет работать сразу. Сохранённое агентом останется в черновике."},
	"templates.saved.revoked": {EN: "The agent's access was revoked.", RU: "Доступ агента отозван."},
}

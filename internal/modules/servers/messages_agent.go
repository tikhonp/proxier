package servers

import "github.com/tikhonp/proxier/internal/platform/i18n"

// agentMessages are the texts of the agent hand-off. What the agent itself
// reads (the prompt, the API's answers) is English and lives in servers/agent.
var agentMessages = i18n.Messages{
	"job.servers.agent_sessions":             {EN: "Close expired agent sessions", RU: "Закрыть истёкшие сеансы агентов"},
	"job.servers.agent_sessions.step.expire": {EN: "Close expired sessions", RU: "Закрыть истёкшие сеансы"},

	"agent.ctx.report":       {EN: "Validation report", RU: "Отчёт проверки"},
	"agent.ctx.report.note":  {EN: "computed when you create the hand-off", RU: "считается при создании передачи"},
	"agent.ctx.job":          {EN: "Failed job log", RU: "Журнал упавшей задачи"},
	"agent.ctx.job.none":     {EN: "None", RU: "Нет"},
	"agent.ctx.job.item":     {EN: "{server} · job #{id} · step {step}", RU: "{server} · задача #{id} · шаг {step}"},
	"agent.ctx.job.help":     {EN: "Jobs of servers built from this template. The log is the stored one, with secrets already hidden.", RU: "Задачи серверов, построенных из этого шаблона. Журнал — сохранённый, секреты в нём уже скрыты."},
	"agent.ctx.preview":      {EN: "Preview for {name}", RU: "Предпросмотр для {name}"},
	"agent.ctx.preview.note": {EN: "rendered files, secrets masked", RU: "готовые файлы, секреты скрыты"},
	"agent.ctx.always":       {EN: "The manifest reference and the validator rules are always included.", RU: "Справочник манифеста и правила проверки доступны всегда."},

	"agent.can.1":  {EN: "read and write this draft", RU: "читать и менять этот черновик"},
	"agent.can.2":  {EN: "validate it", RU: "проверять его"},
	"agent.can.3":  {EN: "preview it for the servers you tick", RU: "смотреть его для отмеченных серверов"},
	"agent.can.4":  {EN: "read the context above", RU: "читать контекст выше"},
	"agent.cant.1": {EN: "publish or discard", RU: "публиковать или отбрасывать"},
	"agent.cant.2": {EN: "deploy to any server", RU: "выкатывать на серверы"},
	"agent.cant.3": {EN: "see a secret value", RU: "видеть секретные значения"},
	"agent.cant.4": {EN: "touch anything else", RU: "трогать что-либо ещё"},

	"agent.err.job":    {EN: "That job does not belong to a server built from this template.", RU: "Эта задача не относится к серверу из этого шаблона."},
	"agent.err.server": {EN: "That server is not an active server of this template.", RU: "Этот сервер не активен или построен из другого шаблона."},

	"agent.to_template":       {EN: "Back to the template", RU: "К шаблону"},
	"agent.secret.revoke":     {EN: "To end it sooner, revoke access on the template page.", RU: "Чтобы закончить раньше, отзовите доступ на странице шаблона."},
	"agent.band.counts":       {EN: "{saves} saves · {validations} validations", RU: "сохранений: {saves} · проверок: {validations}"},
	"templates.saved.revoked": {EN: "The agent's access was revoked.", RU: "Доступ агента отозван."},
}

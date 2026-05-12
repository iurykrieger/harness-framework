# Lib Refactor — Centralização e Remoção de Duplicação

**Data:** 2026-05-12
**Status:** Aprovado para implementação (escopo: todas as 7 fatias)
**Branch:** `refactor` (worktree)

## Objetivo

Reduzir duplicação massiva entre os scripts dos skills (`start`, `stop`, `list`, `tail`, runners), remover código morto em `lib/sensor`, e elevar primitivas reutilizadas para `lib/` com APIs estáveis. **Sem mudança de comportamento observável** — todos os Signals continuam validando contra `schemas/signal.json`; todos os testes existentes continuam passando após adaptação aos novos call sites.

## Não-objetivos

- Alterar schemas (`schemas/*.json`).
- Alterar contratos externos: shape dos Signals emitidos, exit codes, formato JSONL.
- Mexer em hooks (`hooks/error-issue-autofiler.go`, `hooks/setup-failure-detector.go`).
- Refatorar `lib/orchestrator`, `lib/heal`, `lib/subprocess`, `lib/registry/{state,liveness,lock,paths,sanitize}` — escopo já bem desenhado.

## Achados (resumo)

### A. Código morto / APIs sobrepostas em `lib/sensor`

- `LoadAndValidateSensor` (`lib/sensor/load.go:15`) — zero callers de produção.
- `readJSONFile` (`lib/sensor/load.go:37`) — usado apenas por `LoadAndValidateSensor`.
- Três funções com fronteira borrada: `FindSensorByID`, `ResolveByID`, `ResolveSensorPath`.

### B. Duplicação em 4 scripts de skill (~200 LoC)

| Padrão | Skills | LoC |
|---|---|---|
| Bootstrap de `main()` | start, stop, list, tail | ~72 |
| `validateSignal` helper | start, stop, list, tail | ~80 |
| Builders de envelope manual (`simpleSignal`/`finalSignal`) | start, stop, list, tail | ~68 |
| `holderSummaries` | stop, list | ~26 |
| `stringField`, `loadSensorJSON` | start, stop, runners | ~30 |

### C. `lib/` subutilizado

- `sensor.BuildEnvelope` ignorado por `start.go:200-206`.
- `schema.LoadValidator(...)` + check repetido em 5 skills.
- `registry.DiagnoseMetadata(res)` aplicado manualmente em 8+ pontos.
- `resolveSchemasDir` (`lib/sensor/persist.go:79`) duplica lógica de `lib/schema/discover.go`.

---

## Design

### Princípio de organização

`lib/` continua organizado por contexto (`sensor/`, `signal/`, `registry/`, `schema/`, `cli/`). Cada novo helper vai para o contexto correto pelo seu domínio:

- Construir/validar **Signals** → `lib/signal/`
- Bootstrap de skill (cwd + registry + validator) → `lib/cli/`
- Resolver path de sensor → `lib/sensor/`
- Sumarizar holders → `lib/registry/`

### Fatia 1 — Remover dead code (`lib/sensor/load.go`)

Apagar:
- `LoadAndValidateSensor` e seu teste (se existir; o grep mostrou nenhum).
- `readJSONFile` (usado só por `LoadAndValidateSensor`).
- Arquivo `lib/sensor/load.go` se ficar vazio.

**Impacto:** ~50 LoC removidas. Zero callers de produção afetados.

### Fatia 2 — Consolidar resolução de path de sensor

Unificar três funções em uma:

```go
// Em lib/sensor/path.go
//
// Resolve devolve o path absoluto canônico para um sensor identificado por
// um id puro ("my-sensor"), um caminho prefixado ("@sensors/my.json"), ou
// um caminho relativo/absoluto. baseDir é o projectRoot; ids puros são
// resolvidos como <baseDir>/sensors/<id>.json.
func Resolve(idOrPath, baseDir string) (string, error)
```

Heurística:
- Se contém `/`, `\` ou começa com `@` → trata como path (lógica atual de `ResolveSensorPath`).
- Senão se bate `^[a-z][a-z0-9-]*$` → trata como id (lógica atual de `ResolveByID`).
- Caso contrário → erro descritivo.

**Migração:**
- `orchestrator/dag.go:48` (`FindSensorByID`) → `sensor.Resolve(id, sensorRoot)` — atenção: aqui `sensorRoot` é o diretório `sensors/`, não o projectRoot. **Solução:** manter `FindSensorByID` privado (renomear para `findInDir`) ou exigir que o orchestrator passe o projectRoot. Vou **renomear `FindSensorByID` para `resolveInDir` (não exportado)** e deixar `Resolve` lidar com o caso "projectRoot + sensors/".
- Todos os skills que chamam `ResolveByID` → `sensor.Resolve(id, projectRoot)`.
- Remover `ResolveSensorPath` (consumido apenas por dead code).

**Impacto:** ~40 LoC removidas. Uma API pública em vez de três.

### Fatia 3 — `signal.Builder` + `signal.ValidateOrEmergency`

Em `lib/signal/`:

```go
// Em lib/signal/builder.go
type Builder struct { /* ... */ }

func NewBuilder(sensorID, version string) *Builder
func (b *Builder) WithVerdict(v, severity string) *Builder
func (b *Builder) WithKind(kind string) *Builder
func (b *Builder) WithRationale(s string) *Builder
func (b *Builder) WithEvidence(ev []interface{}) *Builder
func (b *Builder) WithMetadata(extra map[string]interface{}) *Builder
func (b *Builder) WithDiagnose(diagnose map[string]interface{}) *Builder
func (b *Builder) WithLatencyMS(ms int) *Builder
func (b *Builder) WithRunID(runID, startedAt, finishedAt string) *Builder // override quando vier de envelope
func (b *Builder) Build() map[string]interface{}
```

Defaults aplicados em `Build()`:
- `confidence: 1.0`
- `cost_actual.latency_ms: 0` (a menos que `WithLatencyMS` seja chamado)
- `run_id: uuid.NewString()`, `started_at = finished_at = NowFn()` (a menos que `WithRunID` seja chamado)
- `evidence: [{rationale: ...}]` se `WithRationale` foi chamado e `WithEvidence` não foi.

```go
// Em lib/signal/validate.go
//
// ValidateOrEmergency valida sig contra schemas/signal.json. Se a validação
// falhar, retorna um signal de emergência (kind=signal_validation_failed) e
// loga o erro em stderr. Caso contrário retorna sig sem cópia.
func ValidateOrEmergency(v *schema.Validator, sig map[string]interface{}, fallbackID string, stderr io.Writer) map[string]interface{}
```

**Migração:** substitui 4 cópias de `validateSignal()` e 6 builders ad-hoc (`simpleSignal`, `simpleErrSignal`, `errorListSignal`, `finalSignal`, `tailEnvelope`, `buildAggregate`).

**Impacto:** ~150 LoC removidas. Mudança em um campo de envelope agora é one-place.

### Fatia 4 — `cli.Bootstrap(skillName)`

Em `lib/cli/bootstrap.go`:

```go
// Bootstrap executa o setup padrão de qualquer skill que toca o registry:
// 1. resolve cwd
// 2. registry.LookupSanitized
// 3. emite signals de discovery_error / migrated quando aplicável (em stdout)
// 4. inicializa o schema validator
//
// Retorna o resultado pronto para uso. Se exitCode != 0, o caller deve sair
// imediatamente (signals já foram emitidos).
type BootstrapResult struct {
    Res       registry.Result
    Validator *schema.Validator
    Diagnose  map[string]interface{} // já contém DiagnoseMetadata(res)
    ExitCode  int                    // != 0 quando bootstrap falhou
}

func Bootstrap(skillName string, stdout, stderr io.Writer) BootstrapResult
```

**Migração:** os `main()` de start/stop/list/tail viram:

```go
func main() {
    b := cli.Bootstrap("start-sensor", os.Stdout, os.Stderr)
    if b.ExitCode != 0 { os.Exit(b.ExitCode) }
    os.Exit(runStart(b, os.Args[1:]))
}
```

**Impacto:** ~72 LoC removidas. Novos skills herdam o entry point grátis.

### Fatia 5 — `registry.SummarizeHolders`

Em `lib/registry/held_by.go` (anexar):

```go
// SummarizeHolders converte holders em representação JSON serializável.
// Quando opts.DeadOnly=true devolve apenas kind=sensor com pid morto.
type SummarizeOpts struct {
    DeadOnly bool
}
func SummarizeHolders(holders []HeldByEntry, opts SummarizeOpts) []interface{}
```

**Migração:** substitui `holderSummaries` (stop, list) e `deadHolderSummaries` (stop).

**Impacto:** ~25 LoC removidas.

### Fatia 6 — `start.go` usa `sensor.BuildEnvelope`

Substituir literal `libsensor.Envelope{...}` (`start.go:200-206`) por `sensor.BuildEnvelope(sensorJSON)`. Já existe e já é testado.

**Impacto:** ~8 LoC removidas. Garante que mudanças em envelope (campo novo, formato de timestamp) só precisam de um patch.

### Fatia 7 — Consolidar `resolveSchemasDir`

`lib/sensor/persist.go:79` walka up procurando `schemas/`. `lib/schema/discover.go` (via `LoadValidator("", stderr)`) faz a mesma coisa. Mover para `lib/schema/` (já tem `discover.go`), exportar como `schema.DiscoverDir(start string) string` e fazer `persist.go` chamar.

**Impacto:** ~15 LoC removidas. Single source of truth para descoberta de schemas.

---

## Plano de PRs

**PR 1 — Cleanup baixo risco** (fatias 1, 2, 5, 6, 7):
- Remove dead code.
- Consolida APIs sobrepostas.
- Não toca em entry points nem em construção de Signal.
- Risco de regressão: baixo (testes de orchestrator e dos skills cobrem os call sites afetados).

**PR 2 — Signal builder + bootstrap unificado** (fatias 3, 4):
- Maior impacto em LoC removidas.
- Toca todas as skills que emitem signal (start, stop, list, tail).
- Requer: testes de skill atualizados, golden tests dos shapes de signal preservados.
- Risco de regressão: médio. Mitigação: rodar todos os testes existentes antes/depois sem alterá-los; só os call sites mudam.

## Definition of Done

1. `go test ./lib/... && go test -tags=run_computational ./skills/... && go test -tags=run_inferential ./skills/... && go test -tags=start_sensor ./skills/... && go test -tags=stop_sensor ./skills/... && go test -tags=list_sensors ./skills/... && go test -tags=tail_sensor ./skills/...` todos verdes.
2. `go vet -tags=run_computational ./... && go vet -tags=run_inferential ./...` sem warnings.
3. `LoadAndValidateSensor`, `readJSONFile`, `ResolveSensorPath`, `FindSensorByID` exportado, `validateSignal` em scripts, `simpleSignal/simpleErrSignal/errorListSignal/finalSignal` em scripts, `holderSummaries/deadHolderSummaries` em scripts: **removidos** ou tornados não-exportados conforme aplicável.
4. Linha do tempo de Signals (shape, ordem, conteúdo de `metadata.kind`/`metadata.cause`) idêntica antes e depois para um conjunto de testes de fumaça (start → list → tail → stop em ciclo).
5. Cada PR independente: PR 1 não depende do PR 2.
6. Total estimado: **~360 LoC líquidas removidas** (medidas via `git diff --stat main`).

## Riscos e mitigações

- **`signal.Builder` muda a ordem das chaves em `metadata`** — Go `map[string]interface{}` já não garante ordem; `encoding/json` ordena chaves alfabeticamente. Sem impacto observável (consumidores parseiam JSON, não comparam strings).
- **`cli.Bootstrap` muda o ponto de validação do validator** — se um skill quer schemas-dir custom, precisa de uma sobrecarga ou flag. Mitigação: `cli.BootstrapWithSchemas(skillName, schemasDir, ...)`.
- **Renomear `FindSensorByID` para não exportado quebra `orchestrator`** — o orchestrator ainda precisa do helper. Decisão: manter `FindSensorByID` privado em `lib/sensor` mas exportar `Resolve` como a API pública. Orchestrator passa por `Resolve(id, sensorRoot)` se quisermos uma API só, **ou** mantemos `findInDir` privado e o orchestrator usa via um pequeno wrapper. Vamos definir no PR 1.

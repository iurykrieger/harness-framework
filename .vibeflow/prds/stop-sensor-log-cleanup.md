# PRD: Cleanup de logs no /stop-sensor

> Generated via /vibeflow:discover on 2026-05-11

## Problem

Hoje `/stop-sensor` documenta explicitamente que `.runtime/sensors/<id>/{raw.log, signals.log}` NÃO são removidos — preservação para auditoria (`skills/stop-sensor/SKILL.md:38`). O resultado prático em uso real:

1. **`.runtime/sensors/` entulhado.** Após várias sessões, diretórios `<id>/` de sensores que não rodam mais permanecem como ruído estrutural no FS. Visualmente confunde quem inspeciona o runtime, e mentalmente quebra o modelo de "stop = sensor encerrado, sem rastros vivos".
2. **Crescimento de disco em sessões longas.** Um `raw.log` de dev server / observador blocking acumula horas de stdout/stderr e chega a tamanhos não-triviais. O usuário precisa hoje fazer `rm -rf` manual para reset.
3. **`/tail-sensor` mostrando "fantasmas" após stop.** Como `signals.log` sobrevive ao stop, um agente que tente tailar depois do encerramento recebe sinais antigos do run que já terminou, podendo interpretar como produção nova.

A política atual de "preserve para audit" entra em conflito direto com essas dores. Já há sinal de fricção em `skills/start-sensor/SKILL.md:56`, que aconselha o usuário a "periodicamente `/stop-sensor`/`/start-sensor` para conter o crescimento" — mitigação incongruente, já que só funciona porque `start-sensor` trunca os logs no próximo start (`start.go:152,159`), não porque `stop-sensor` limpa.

## Target Audience

Desenvolvedores usando o harness durante implementação ou debug, e o próprio agente Claude Code quando orquestra `/start-sensor` + `/tail-sensor` + `/stop-sensor` em loop. Sobem 1–N sensores blocking em sessão, param, recomeçam, e ao longo de horas ou dias acumulam diretórios em `.runtime/sensors/`.

## Proposed Solution

Quando `/stop-sensor` encerrar um sensor com sucesso (Signal `metadata.kind=aggregate` emitido em stdout), remover recursivamente `.runtime/sensors/<id>/` **após** o aggregate ter sido lido de `signals.log`, construído e emitido. A remoção é parte do happy path único.

Em todos os outros caminhos de saída (`not_running`, `held`, `failed`), o diretório permanece intocado.

## Success Criteria

1. Após `/stop-sensor <id>` com `metadata.kind=aggregate`, o diretório `.runtime/sensors/<id>/` não existe mais no FS.
2. O Signal aggregate no stdout continua válido contra `schemas/signal.json` e contém o mesmo conteúdo de antes (a leitura de `signals.log` precisa acontecer ANTES da remoção).
3. Em `/stop-sensor` com saída `not_running`, `held` ou `failed`, `.runtime/sensors/<id>/` continua intacto.
4. `skills/stop-sensor/SKILL.md:38` e `skills/start-sensor/SKILL.md:56` atualizados para refletir a nova política — desaparece o aviso de "logs cresçem unboundedly" e a nota "logs are NOT deleted" vira "logs são removidos no stop bem-sucedido".
5. `/tail-sensor <id>` em um sensor recém-parado retorna erro `registry_exists=false` (sensor não está no registry), em vez de devolver linhas órfãs do run anterior.

## Scope v0

- Em `skills/stop-sensor/scripts/stop.go`, no caminho `kind=aggregate` (linha ~277): após construir o aggregate Signal, removê-lo do registry, e antes de retornar, invocar `os.RemoveAll(r.SensorDir(id))`.
- Cleanup é **best-effort**: falha de `os.RemoveAll` (permissão, handle aberto, FS bloqueado) registra um campo `metadata.cleanup_warning` no aggregate Signal e segue. NÃO muda o verdict (que reflete só o resultado de parar o subprocess).
- Tests em `skills/stop-sensor/scripts/stop_test.go` cobrindo:
  - (a) cleanup acontece em sucesso (`kind=aggregate`) — diretório some.
  - (b) preservação em `not_running`, `held`, `failed` — diretório permanece.
  - (c) aggregate Signal continua validando contra schema após cleanup.
  - (d) `cleanup_warning` é registrado quando `RemoveAll` falha, sem alterar verdict.
- Atualizar `skills/stop-sensor/SKILL.md` (Notes) e `skills/start-sensor/SKILL.md` (Notes & limits) para refletir a nova política.

## Anti-scope

- **Não** mexer em `running_sensors.json` (registry) — esse arquivo continua sendo cleanup já tratado em sucesso, num caminho separado.
- **Não** introduzir flag `--clean` / `--keep-logs`. A política é única: stop bem-sucedido limpa, ponto. Se um dia precisar reverter, abre nova PRD.
- **Não** apagar diretórios órfãos em `not_running` (sensor morto sozinho). Fica como Open Question abaixo.
- **Não** rotacionar/truncar logs durante o run. Outro problema, fora desta PRD.
- **Não** preservar conteúdo via cópia para um "arquivo histórico". O aggregate Signal já sumariza o run; granularidade individual (linha-a-linha de `raw.log`, `signals.log`) é descartada conscientemente.
- **Não** alterar o comportamento de `start-sensor` (continua truncando raw.log/signals.log no start — agora redundante no caminho feliz, mas defensivo contra órfãos).

## Technical Context

- `stop.go` hoje (linha 277): no `kind=aggregate` ele já chama leitura de `signals.log` → aggregate → remove entry do registry → retorna. O cleanup novo entra **depois** de tudo isso, antes do return.
- `signals.log` precisa ser **lido** antes de qualquer `RemoveAll`. Ordem importa.
- `lib/registry/paths.go:36` expõe `Root.SensorDir(id)` — esse é o alvo correto de `os.RemoveAll`. Não derivar paths à mão.
- Política do framework: "Signal emitido em stdout = ponto de não retorno". Cleanup é efeito colateral pós-aggregate e jamais deve impedir a emissão do Signal.
- `start.go:152,159` já trunca raw.log e signals.log no início via `os.WriteFile(path, nil)`. Permanece como cinto-e-suspensórios — sobrevive a um cenário em que o stop anterior crashou antes do RemoveAll.
- Convenções do projeto: lógica determinística em Go, tests no mesmo dir, build tag `//go:build stop_sensor`. PRD respeita.

## Open Questions

- **Diretórios órfãos de sensores que morreram sozinhos.** Se um sensor crasha sem `/stop-sensor`, e o usuário roda `/stop-sensor` depois (recebendo `not_running`), o diretório fica lá indefinidamente — até o próximo `/start-sensor` do mesmo id. Vale resolver em PRD separada: opção (a) em `not_running`, se a entry do registry não existe E não há watcher vivo apontando pra esse dir, apagar; ou opção (b) skill nova `/clean-sensor-runtime` que faz GC. Não bloqueia esta PRD.
- **Watcher ainda drenando quando RemoveAll roda.** `stop.go` sinaliza o watcher para drenar antes de ler `signals.log`. Em Linux/macOS, unlink funciona com file handles abertos (inode permanece até o último close). Vale validar em teste com fake watcher segurando handles, para garantir que cleanup não trava nem corrompe o aggregate.

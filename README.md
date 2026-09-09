# portscout

Descobre portas em LISTEN numa máquina remota e escreve as entradas no YAML do
[tunnel9](https://github.com/sio2boss/tunnel9), que cuida da TUI, do liga/desliga, do
monitoramento e do túnel em si.

`portscout` nunca abre um túnel. Ele só olha o remoto e edita o YAML.

## Por quê

O VSCode Remote descobre as portas abertas no remoto e oferece encaminhá-las. Nenhuma
ferramenta de terminal faz a parte da descoberta: o tunnel9 já tem TUI, toggle, monitoramento
e remap, mas os túneis são escritos à mão. Isto preenche só essa lacuna.

## Uso

```sh
portscout list  dev     # descobre e imprime, sem escrever
portscout scan  dev     # descobre e atualiza o YAML do tunnel9
portscout watch dev     # scan a cada 15s
```

`dev` é qualquer destino que o seu `ssh` entenda — o `~/.ssh/config` é a fonte de máquinas.

Depois de um `scan`, abra o `tunnel9`, filtre pela tag `auto` e aperte `Enter` no que quiser
tunelar.

## O que ele descobre

Une `ss -ltnpH` (processos do host) com `docker ps` (containers) numa única sessão SSH.

As duas fontes são necessárias: com Docker rootless, **toda** porta publicada aparece no `ss`
como o processo `rootlesskit`, sem identificar o container. O nome utilizável só vem do
`docker ps`, casado pela porta do host.

Filtragem de ruído, com `--all` para desligar:

- Por nome de processo: `sshd`, `systemd-resolved`, `chrony`, `cups`, `avahi`, `postfix`…
- Por número de porta, para os casos em que o `ss` não consegue ler o processo — o que
  acontece sempre que você não é root no remoto: 22, 25, 53, 111, 123, 631, 5353, 5355.
- Portas sem rótulo a partir de 32768, que são portas efêmeras de cliente, não serviços.

Uma porta **com** rótulo nunca é filtrada por número: um dev server numa porta alta é
exatamente o que você quer alcançar.

## Os nomes das entradas

O `ss` só mostra os 15 primeiros caracteres do nome de um processo, que é o limite do
kernel — `next-server (v16.3.4)` chega como `next-server (v1`. O nome inteiro é lido do
`/proc/<pid>/cmdline`, e só é usado quando de fato continua o nome truncado.

Um container que publica mais de uma porta repete o mesmo nome em cada uma. Só os nomes
que se repetem ganham a porta remota como sufixo — `mailpit-1 (3104)` e `mailpit-1 (3105)`
— para não haver duas linhas idênticas na TUI.

## Regras de convivência com o seu YAML

- Só mexe em entradas com `tag: auto` (mude com `--tag`). O resto do arquivo é intocável.
- O `local_port` que você editar é preservado nas próximas varreduras; só o alias é atualizado.
- Porta que sumiu do remoto **não** é apagada, a menos que você passe `--prune`.
- Se a porta local desejada estiver ocupada — por outra entrada, por outra porta desta mesma
  varredura, ou por algo escutando na sua máquina — escolhe a próxima livre e avisa.
- Comentários, ordem das chaves e indentação de dois espaços são preservados.
- Escrita atômica; se o YAML não parsear, aborta sem gravar.

## Flags

Podem vir antes ou depois do host.

| Flag | Padrão | Efeito |
| --- | --- | --- |
| `--all` | `false` | Não filtra portas de sistema |
| `--prune` | `false` | Remove entradas gerenciadas cuja porta sumiu |
| `--tag` | `auto` | Tag das entradas gerenciadas |
| `--config` | padrão do tunnel9 | Caminho do YAML |
| `--every` | `15s` | Intervalo do `watch` |

## Instalação

```sh
go install github.com/MarllonGomes/portscout/cmd/portscout@latest
```

Ou baixe o binário da página de releases.

O portscout só edita o YAML — quem abre o túnel é o
[tunnel9](https://github.com/sio2boss/tunnel9), então ele precisa estar instalado:

```sh
brew install sio2boss/tap/tunnel9
# ou
bash -c "$(curl -fsSL https://raw.githubusercontent.com/sio2boss/tunnel9/main/tools/install.sh)"
```

## Desenvolvimento

```sh
go test ./...      # nenhum teste abre ssh ou rede
go vet ./...
```

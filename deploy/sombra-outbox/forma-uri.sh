#!/usr/bin/env bash
# Mostra a FORMA da URI do Mongo (variável URI), nunca o valor: esquema, usuário, se há senha,
# quantos @, se sobrou < > do modelo, espaços/quebras de linha e se há host. Usuário não é segredo.
set -uo pipefail
u=${URI-}
scheme=outro; case "$u" in mongodb+srv://*) scheme=mongodb+srv;; mongodb://*) scheme=mongodb;; esac
rest=${u#*://}
userinfo=""; [[ "$rest" == *@* ]] && userinfo=${rest%@*}
user=${userinfo%%:*}
hostpart=${rest##*@}; hostpart=${hostpart%%/*}; hostpart=${hostpart%%\?*}
ats=$(printf '%s' "$u" | tr -cd '@' | wc -c)
echo "uri: esquema=$scheme"
if [ "$user" = kas_outbox_shadow ]; then echo "uri: usuário=kas_outbox_shadow"; else echo "uri: usuário diferente de kas_outbox_shadow (ou ausente)"; fi
[[ "$userinfo" == *:* && -n "${userinfo#*:}" ]] && echo "uri: senha presente" || echo "uri: senha AUSENTE"
echo "uri: quantidade de @ = $ats (esperado 1)"
[[ "$u" == *"<"* || "$u" == *">"* ]] && echo "uri: contém < ou > (sobrou do modelo)" || echo "uri: sem < >"
[[ "$u" =~ [[:space:]] ]] && echo "uri: contém espaço ou quebra de linha" || echo "uri: sem espaços"
if [ -z "$hostpart" ]; then echo "uri: host AUSENTE"
elif [[ "$hostpart" == *.mongodb.net ]]; then echo "uri: host termina em .mongodb.net"
else echo "uri: host não termina em .mongodb.net"; fi
[ "$scheme" = mongodb+srv ] && [[ "$hostpart" == *,* || "$hostpart" == *:* ]] && echo "uri: mongodb+srv não aceita porta nem lista de hosts"
exit 0

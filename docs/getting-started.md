# Prise en main : comprendre ce que tezcatl fait de vos logs

Ce guide s'adresse à un administrateur système. Il ne suppose aucune
familiarité avec les statistiques et n'en demandera aucune : les quelques
notions utiles sont expliquées quand elles servent. À la fin, vous saurez
ce que tezcatl détecte, comment il s'y prend, et surtout ce qu'il ne sait
pas faire.

Comptez vingt minutes, dont cinq de manipulation. Aucun serveur, aucun
cluster, aucune configuration.

## En une phrase

Tezcatl apprend à quoi ressemblent vos logs et vos métriques quand tout
va bien, puis signale ce qui s'en écarte.

C'est tout. Il ne sait pas ce qu'est une base de données, un timeout ou
un certificat expiré. Il ne lit pas vos messages, il les compare à ce
qu'il a déjà vu.

## Ce qu'il ne fait pas

Autant l'établir tout de suite, ça évitera des déceptions.

**Il ne remplace pas votre supervision.** Si votre API doit répondre en
moins de 300 ms et que ça compte pour vos utilisateurs, écrivez une
alerte déterministe là-dessus. Tezcatl sert à l'inverse : trouver ce que
vous n'avez pas pensé à surveiller.

**Il ne trouve pas la cause.** Il vous dira qu'un message inédit est
apparu quatre minutes après un déploiement. Il ne vous dira jamais que le
déploiement a causé le message, parce qu'il n'en sait rien.

**Il ne détecte pas ce qui ressemble au normal.** Un service qui rend
des réponses fausses avec le même volume de logs et les mêmes temps de
réponse ne déclenchera rien. Un service cassé depuis avant l'installation
de tezcatl sera appris comme normal.

**Son silence ne prouve rien.** Aucun événement ne veut dire « rien ne
s'écarte de ce que j'ai appris », pas « tout va bien ».

Si vous gardez ces quatre phrases, le reste du guide vous sera facile.

## Le trajet d'une ligne de log

Cinq étapes, du texte brut au récit.

```
ligne de log  ──►  template  ──►  signal  ──►  événement  ──►  incident
   (texte)       (le motif)    (l'écart)     (les écarts    (les événements
                                             groupés)        d'une période)
```

1. **Une observation** arrive : une ligne de log, un relevé de métrique,
   un déploiement déclaré.
2. La ligne rejoint un **template**, le motif dont elle est une
   instance.
3. Les **détecteurs** comparent l'observation à ce qu'ils ont appris sur
   ce template ou cette métrique, et produisent des **signaux**.
4. Les signaux proches dans le temps sur le même service fusionnent en
   un **événement**.
5. `tezcatl incidents` regroupe les événements d'une période en
   **incidents**.

Vous n'avez à retenir que ceci : un signal est un constat élémentaire,
un événement est ce qu'on vous montre, un incident est un récit.

## Cinq minutes de pratique

Fabriquons des logs. Un service qui répond normalement pendant cinquante
minutes, puis tombe.

```bash
cat > gen.sh <<'EOF'
#!/bin/sh
t=1788100000
i=0
while [ $i -lt 600 ]; do
    t=$((t + 5))
    printf '%s INFO  GET /api/cart 200 in %dms\n' "$(date -u -d @$t +%Y-%m-%dT%H:%M:%SZ)" $((10 + i % 7))
    i=$((i + 1))
done
t=$((t + 5))
printf '%s ERROR database connection timeout after 30s\n' "$(date -u -d @$t +%Y-%m-%dT%H:%M:%SZ)"
printf '%s ERROR database connection timeout after 30s\n' "$(date -u -d @$((t+1)) +%Y-%m-%dT%H:%M:%SZ)"
EOF
sh gen.sh > demo.log
```

Six cent deux lignes, dont deux qui sortent du lot. Donnons-les à
tezcatl :

```bash
tezcatl standalone logs \
  --service boutique --environment demo \
  --replay --state-dir ./demo-state < demo.log | jq .
```

`--replay` fait avancer l'horloge interne sur les timestamps des lignes
plutôt que sur l'heure courante, donc une heure de logs se rejoue en une
seconde. `--state-dir` garde ce qui a été appris.

Une seule ligne sort, et la voici en entier :

```json
{
  "kind": "anomaly.correlated",
  "source": "demo/boutique",
  "timestamp": "2026-08-30T15:16:45Z",
  "severity": "critical",
  "confidence": 0.92,
  "summary": "new log template after learning period: ERROR database connection timeout after 30s (+1 correlated signals)",
  "signals": [
    {"kind": "log.new_template", "score": 0.8,
     "attributes": {"template": "ERROR database connection timeout after 30s", "template_id": "2"}},
    {"kind": "log.rare_template", "score": 0.6,
     "attributes": {"count": "2", "total": "602", "template": "ERROR database connection timeout after 30s"}}
  ],
  "context": {"before": 10, "after": 1},
  "attributes": {"multimodal": "false", "signal_count": "2"}
}
```

Six cents lignes de trafic normal n'ont rien produit. Les deux lignes
d'erreur ont produit **deux signaux** : le message est inédit, et il est
rare (2 occurrences sur 602). Les deux tombant au même endroit, ils ont
fusionné en un seul événement. Le `context` contient les dix lignes
précédentes et la suivante, pour que vous voyiez ce qui entourait la
panne sans retourner dans le fichier.

Regardons ce qu'il a retenu :

```bash
tezcatl templates --state-dir ./demo-state
```

```
PARTITION      ID  SIZE  MARKING  TEMPLATE
demo/boutique  1   600            INFO GET /api/cart 200 in <*>
demo/boutique  2   2              ERROR database connection timeout after 30s
```

Deux motifs, l'un vu 600 fois, l'autre 2 fois. Personne n'a écrit
d'expression régulière.

Maintenant, supposons que ce timeout soit connu, sans intérêt, et que
vous ne vouliez plus l'entendre :

```bash
tezcatl mark --state-dir ./demo-state \
  --template "ERROR database connection timeout after 30s" --as ignore
```

La colonne `MARKING` passe à `ignore` et ce template ne produira plus
rien, maintenant et après redémarrage. C'est la boucle de feedback, et
c'est le seul travail que tezcatl vous demande.

## Les concepts, un par un

### Le template

Un template est le motif d'une ligne, ses parties variables remplacées
par des marqueurs. Six cents lignes différentes par leur temps de
réponse se ramènent à `INFO GET /api/cart 200 in <*>`.

Le regroupement utilise Drain3, un algorithme qui bâtit un arbre de
motifs au fil de l'eau. Il ne connaît pas vos formats, il repère les
positions où le texte varie.

Deux marqueurs différents, qu'il ne faut pas confondre :

- `<NUM>`, `<IP>`, `<HEX>`, `<UUID>`, `<EMAIL>` sont des **masques**
  appliqués avant le regroupement. Une adresse IP est remplacée par
  `<IP>` quoi qu'il arrive, sinon chaque visiteur créerait son propre
  motif ;
- `<*>` marque une **position variable** trouvée par le regroupement
  lui-même. Dans notre exemple, `12ms` n'est pas un nombre pur à cause
  du suffixe, donc le masque `<NUM>` ne s'applique pas ; c'est Drain3
  qui a constaté que ce jeton changeait tout le temps.

Dans les deux cas, ne lisez jamais un marqueur comme une valeur.

Le corollaire, c'est que la qualité du regroupement dépend de vos logs.
Un identifiant qui échappe aux masques, un numéro de version collé à un
mot, une liste dont la longueur varie, et vous obtenez un template par
occurrence. Tezcatl plafonne alors le nombre de templates par source à
2000 et jette les plus anciens, ce qui l'empêche de gonfler mais ne
répare pas les logs.

### La période d'apprentissage

Pendant les cinq premières minutes qui suivent la première ligne d'une
source, tout est normal par construction : tezcatl apprend et se tait.
Ensuite seulement, un motif inédit devient un signal.

Cinq minutes, c'est court pour une machine qui ne dit rien la nuit. Le
réglage est `logs.detection.learning_period`. Sur un service peu bavard,
quinze minutes ou une heure valent mieux.

### Les cinq façons dont un log peut surprendre

| Signal | Ce qu'il veut dire |
| --- | --- |
| `log.new_template` | Un motif jamais vu depuis la fin de l'apprentissage |
| `log.rare_template` | Un motif vu très peu de fois au regard du volume |
| `log.frequency_spike` | Un motif beaucoup plus fréquent que d'habitude |
| `log.missing_template` | Un motif régulier qui a cessé d'apparaître |
| `log.symptomatic_template` | Un motif que vous avez marqué comme digne d'alerte |

Le dernier est le seul qui encode une intention humaine. Les quatre
autres sont des constats sur des comptages.

### La baseline d'une métrique

Pour les métriques, tezcatl ne connaît pas de seuil. Il calcule la
moyenne de ce qu'il a vu et l'écart type, c'est-à-dire l'ampleur
habituelle des variations autour de cette moyenne.

Le **z-score** est la question « à combien d'écarts types de la moyenne
se trouve ce relevé ? ». Un z de 1 est banal. Un z de 3, le seuil par
défaut, veut dire que le relevé est trois fois plus loin de la moyenne
que ne l'est une variation ordinaire.

Reprenons avec une file d'attente stable autour de 5 qui saute à 180 :

```bash
{ i=0; while [ $i -lt 60 ]; do printf 'queue_depth %d\n' $((4 + i % 3)); i=$((i+1)); done
  printf 'queue_depth 180\n'; } > demo.metrics

tezcatl standalone metrics --service boutique --environment demo \
  --state-dir ./m-state < demo.metrics | jq -c .summary
```

```
"queue_depth = 180 deviates from baseline 4.99 (z = 211.9)"
```

La moyenne apprise est de 4,99, et 180 s'en écarte de 211 écarts types.
Aucun seuil n'a été écrit nulle part.

Deux propriétés de cette moyenne méritent d'être connues. Elle est
**glissante** : les relevés récents pèsent davantage, les anciens
s'effacent. Un service dont la charge double durablement verra sa
baseline suivre en quelques dizaines de relevés, ce qui est voulu, une
nouvelle normalité étant une normalité. Et elle a besoin d'un
**échauffement** : par défaut trente relevés avant le premier verdict,
parce qu'une moyenne calculée sur trois points ne vaut rien.

### Pourquoi un z-score énorme peut ne rien vouloir dire

Voici le piège principal des méthodes statistiques, et il faut le voir
une fois pour ne plus s'y laisser prendre.

Prenons une métrique très stable : une mémoire à 1,5 % qui monte à
3,12 %.

```
"memory_used_percent = 3.12 deviates from baseline 1.54 (z = 62.0)"
```

Soixante-deux écarts types. Statistiquement, c'est colossal.
Opérationnellement, c'est une machine qui ne fait toujours rien. Le
z-score est grand parce que le dénominateur est minuscule, pas parce
qu'il s'est passé quelque chose.

Tezcatl répond à ça par un **plancher de signification** : un écart
absolu en dessous duquel il ne dit rien, quel que soit le z-score. Le
seul plancher qu'il pose tout seul concerne les pourcentages, dont
l'échelle est connue sans connaître la métrique, et il vaut 5 points.
C'est pour ça que l'exemple ci-dessus, exécuté sans configuration, ne
produit **rien du tout** : 3,12 moins 1,54 fait 1,58 point, en dessous
du plancher.

Le chiffre de 5 points vient d'une mesure, pas d'une intuition. Sur
l'instance de développement du projet, 27 des 34 anomalies de
pourcentage d'une nuit calme bougeaient de moins que ça, et tout ce
qu'un humain aurait regardé passait au-dessus.

Pour toutes vos autres métriques, tezcatl ne peut pas deviner l'échelle.
Une charge système de 0,6 est-elle beaucoup ? Ça dépend du nombre de
cœurs, et il ne le sait pas. À vous de poser les planchers qui comptent :

```yaml
metrics:
  detection:
    min_deltas:
      system.load1: 0.5        # ignorer les frémissements de charge
      "*percent": 5            # le défaut, écrasez-le par 0 pour l'annuler
```

Les clés acceptent les jokers. Attention, le point n'a rien de spécial :
`*percent` attrape `cpu.percent` comme `memory.used_percent`, alors que
`*.percent` raterait le second.

### Pourquoi le silence n'est pas toujours une nouvelle

`log.missing_template` signale un motif qui a cessé d'apparaître. C'est
précieux pour un battement de cœur ou une tâche planifiée, dont
l'absence est un symptôme.

Encore faut-il que le motif soit régulier. Les journaux d'accès d'un site
sans visiteurs, les tentatives de connexion SSH d'un scanner, les lignes
de débogage d'un client bavard arrivent par rafales : ils sont silencieux
la plupart du temps, et leur silence n'apprend rien.

Tezcatl mesure donc la **régularité** des intervalles : leur dispersion
divisée par leur moyenne. Cet indicateur vaut 0 pour un métronome
parfait, 1 pour des arrivées purement aléatoires, davantage pour des
rafales. Au-delà de 0,5, le motif n'est plus attendu.

L'effet est net. Sur trois jours du journal SSH d'une machine réelle,
480 signaux d'absence tombent à 75, et le nombre d'événements passe de
341 à 86, sans qu'aucun autre type de signal ne bouge.

### L'heure de la journée compte

Un cron nocturne qui tourne à trois heures du matin ne doit pas
surprendre, et son absence à midi non plus. Tezcatl apprend donc une
baseline par heure de la journée. Un pic de trafic quotidien à 9 h est
normal ; le même pic à 3 h ne l'est pas.

C'est le réglage `seasonality: hourly`, actif par défaut. Il demande une
journée complète d'observation pour être utile.

### D'un signal à un événement

Les signaux proches dans le temps sur le même service fusionnent en un
événement, dans une fenêtre de 30 secondes par défaut. C'est ce que vous
avez vu au début : deux signaux, un événement.

Quand des signaux de logs et de métriques se retrouvent dans la même
fenêtre, l'événement porte l'attribut `multimodal`. C'est le cas le plus
intéressant, parce que la coïncidence entre un message inédit et une
métrique qui décroche est plus parlante que chacun des deux séparément.

Pour que ça marche, les deux modalités doivent porter la même identité
`environnement/service`. Une métrique rangée sous `prod/checkout-api` ne
fusionnera jamais avec des logs rangés sous `prod/checkout`.

### Les changements

Tezcatl ne devine pas vos déploiements, vous les lui déclarez :

```bash
tezcatl ingest change --service boutique --environment prod \
  --type deployment --change-version "boutique:v2.4.0"
```

Toute anomalie survenant dans le quart d'heure ressort avec ce
changement attaché :

```json
"related_changes": [
  {"change": {"type": "deployment", "version": "boutique:v2.4.0"},
   "offset_seconds": -180}
]
```

Le déploiement a eu lieu 180 secondes avant l'anomalie. **C'est une
proximité dans le temps, rien d'autre.** Tezcatl l'affiche parce que
c'est la première chose qu'un ingénieur voudrait savoir, pas parce qu'il
a la moindre preuve d'un lien. Sur Kubernetes, le plugin `kubernetes`
détecte les rollouts tout seul et vous évite cette déclaration.

### Les incidents

`tezcatl incidents` prend les événements d'une période et les assemble
en récits, avec un déclencheur, une propagation et des preuves agrégées.

Deux événements appartiennent au même incident quand ils sont
**apparentés** : même service, même changement corrélé, ou assez
simultanés pour être un seul problème qui se propage. La simple
proximité dans le temps ne suffit pas, sans quoi une machine qui produit
une anomalie toutes les huit minutes verrait sa nuit entière fondue en un
seul « incident » de cinq heures, ce qui n'apprend rien.

La sortie `--format markdown` est faite pour être donnée à un agent
conversationnel : elle explique son propre schéma et ses propres limites
avant de présenter les données.

## Le travail que ça vous demande

Tezcatl apprend seul, mais il ne trie pas seul ce qui vous intéresse. Le
premier jour d'utilisation est bruyant, c'est normal et c'est même
souhaitable : il vous montre tout ce qui est inhabituel, et vous seul
savez ce qui mérite votre attention.

Trois commandes suffisent :

```bash
tezcatl templates                                     # ce qui a été appris
tezcatl mark --template "..." --as ignore             # taire un motif
tezcatl mark --metric "docker.memory.*" --as ignore   # taire une série
```

Il existe aussi `--as symptomatic`, pour l'inverse : un motif que vous
voulez voir remonter à chaque occurrence, même une fois banalisé. Et
`--as heartbeat`, pour être prévenu quand un motif *cesse* d'arriver :
le cron de sauvegarde, le tick d'un worker. Sans ce marquage, l'absence
d'un template n'est pas signalée, parce que la statistique sait dire
qu'un flux s'est arrêté, pas si quelqu'un y tenait.

Comptez quelques jours de marquage avant que le flux devienne lisible.
Si après une semaine vous marquez encore beaucoup, c'est généralement le
signe qu'une source produit des identifiants dans ses messages et fait
exploser le nombre de templates ; regardez de ce côté avant d'accuser la
détection.

## Ce que tezcatl ne sait pas

Reprenons, maintenant que les mécanismes sont clairs.

**Il ne connaît pas la causalité.** Ni entre deux signaux, ni entre un
changement et une anomalie. Il rapproche, vous interprétez.

**Il ne voit que ce qu'on lui donne.** Un service dont les logs ne sont
pas collectés est invisible, et son silence dans un rapport ne veut rien
dire.

**Un détecteur muet n'est pas un système sain.** Une panne qui ressemble
à la normale apprise ne produit aucun signal. Un service en panne depuis
avant l'apprentissage est appris comme normal.

**Statistiquement extrême n'est pas opérationnellement grave.** Vous
avez vu le z de 62 sur une mémoire à 3 %. Les planchers de signification
existent pour ça, et c'est à vous de les poser sur vos métriques.

**Le score de confiance ne mesure pas la gravité.** Il dit à quel point
le détecteur est sûr qu'il y a écart par rapport à la baseline. Un écart
certain peut être parfaitement anodin.

**Les frontières d'un incident sont une heuristique.** Deux incidents
peuvent former une seule histoire, un incident peut en contenir deux
sans rapport.

**Il n'est pas un outil temps réel de garde.** Sa boucle de détection
travaille à l'échelle de la dizaine de secondes, et son intérêt est de
révéler l'inattendu, pas de réveiller quelqu'un à trois heures du matin.
Pour ça, il vous faut des alertes sur des indicateurs que vous avez
choisis.

## Où aller ensuite

Vous avez de quoi lire une sortie de tezcatl sans lui prêter des
pouvoirs qu'il n'a pas. Pour l'installer pour de bon :

- [Serveur Dokku ou Ubuntu](./deploy-dokku.md) pour une machine unique
  avec ses applications ;
- [Kubernetes](./deploy-kubernetes.md) pour un serveur central dans un
  cluster ;
- [Tutoriel Kubernetes](./tutorial-kubernetes.md) pour surveiller un
  cluster depuis votre poste, sans rien y installer.

Les options de configuration sont toutes documentées, avec leurs valeurs
par défaut, dans [misc/config/standalone.yaml](../misc/config/standalone.yaml).

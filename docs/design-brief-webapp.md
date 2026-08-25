# Design brief — Explorateur d'événements Tezcatl

Brief destiné à la conception des maquettes (Claude design). Le but est
de définir le quoi et le pourquoi ; le comment visuel est ouvert, dans
les limites de la direction donnée en fin de document.

## 1. Contexte produit

Tezcatl (« miroir » en nahuatl) est un moteur d'observabilité qui
apprend le comportement normal d'applications (templates de logs,
baselines de métriques), détecte les écarts et produit des **événements
corrélés** : une anomalie contextualisée, avec ses preuves, les logs
autour, et les déploiements récents. Le moteur est un binaire headless ;
ses événements sortent aujourd'hui en JSON via stdout, PostgreSQL ou
webhook, et se lisent dans un terminal.

La webapp à concevoir est le **miroir visuel** de ces événements : les
recevoir (webhook), les explorer, les trier, agir dessus. Elle devra
aussi exposer un serveur MCP pour qu'un agent LLM puisse analyser un
événement à la demande — l'UI doit prévoir la place de ces analyses.

Nom de code suggéré : **Itztli** (« obsidienne », le matériau du miroir
de Tezcatlipoca). Libre au design de proposer autre chose.

## 2. Utilisateurs et moments d'usage

Un opérateur solo ou une petite équipe (2-5 personnes), profil
développeur/SRE, qui héberge ses propres services (Dokku, Kubernetes).
Pas de multi-tenant, pas de gestion de rôles.

Trois moments d'usage, qui dimensionnent trois ambiances :

1. **L'astreinte** (mobile, 3h du matin) : une notification webhook a
   sonné. Il faut comprendre en 30 secondes : quel service, quelle
   gravité, qu'est-ce qui a changé juste avant.
2. **La revue du matin** (desktop, café) : qu'est-ce qui s'est passé
   cette nuit ? Balayage rapide, triage, marquage du bruit.
3. **Le post-mortem** (desktop, concentration) : reconstruire la
   chronologie d'un incident, éventuellement lancer l'analyse agent,
   copier des éléments dans un document.

Un état important : **le calme**. La plupart du temps il n'y a aucun
événement, et c'est une bonne nouvelle. L'écran vide doit être conçu
comme un état positif (« tout est normal depuis 3 jours »), pas comme un
échec de l'interface.

## 3. La donnée : l'événement

Tout tourne autour d'un seul objet, reçu par webhook (un POST JSON par
événement). Exemple réel :

```json
{
  "id": "90576ed56393bdcbd447552c466a3646",
  "kind": "anomaly.correlated",
  "source": "prod/checkout",
  "service": "checkout",
  "environment": "prod",
  "timestamp": "2026-08-24T14:05:00Z",
  "severity": "critical",
  "confidence": 0.92,
  "summary": "new log template after learning period: database connection timeout after 30s (+1 correlated signals)",
  "signals": [
    {
      "kind": "log.new_template",
      "modality": "log",
      "timestamp": "2026-08-24T14:05:00Z",
      "score": 0.8,
      "summary": "new log template after learning period: database connection timeout after 30s",
      "attributes": {"template": "database connection timeout after <NUM>s", "occurrences": "2"}
    },
    {
      "kind": "metric.threshold",
      "modality": "metric",
      "score": 0.9,
      "summary": "pool_usage_percent = 97 above configured maximum 90",
      "attributes": {"metric": "pool_usage_percent", "value": "97", "max": "90"}
    }
  ],
  "related_changes": [
    {
      "change": {"type": "deployment", "version": "checkout:v1.8.2", "summary": "rollout via CI"},
      "timestamp": "2026-08-24T14:02:00Z",
      "offset_seconds": -180
    }
  ],
  "context": {
    "before": ["… 10 observations (logs/métriques) précédant l'anomalie …"],
    "after": ["… 10 observations suivantes …"]
  },
  "attributes": {"multimodal": "true", "signal_count": "2"}
}
```

Vocabulaire à refléter dans l'UI :

- **Sévérité** : `info`, `warning`, `critical` (3 niveaux, pas plus).
- **Confiance** : 0 à 1. À montrer sans en faire un gadget (l'opérateur
  doit pouvoir s'y fier, pas la contempler).
- **Signaux** : les preuves élémentaires, chacun avec un score, un
  résumé en une phrase et des attributs chiffrés (valeur, baseline,
  écart). Types : `log.new_template`, `log.rare_template`,
  `log.frequency_spike`, `log.missing_template`,
  `log.symptomatic_template`, `metric.zscore`, `metric.threshold`,
  `metric.trend_drift`.
- **Changements liés** : déploiements/configs récents, avec un décalage
  temporel (« 3 min avant »). Règle produit ferme : **c'est une
  corrélation, jamais une cause**. Le design ne doit pas suggérer de
  causalité (pas de flèche « a causé »).
- **Contexte** : les logs (avec template et niveau) et métriques juste
  avant/après. C'est la matière du post-mortem.
- **Multimodal** : un événement mêlant logs ET métriques vaut plus
  cher ; ça mérite un marqueur discret.

Volumes : faibles. Quelques événements par jour en régime normal, des
rafales de dizaines pendant un incident. Le design peut privilégier la
densité d'information par événement plutôt que la pagination massive.

## 4. Écrans à maquetter

### A. Flux (inbox)

La liste des événements, du plus récent au plus ancien. Par ligne :
sévérité, service/environnement, résumé, heure relative, confiance,
badge multimodal, indication de changement lié (« deploy −3 min »).
Filtres persistants : service, environnement, sévérité, période, type de
signal. Une mini-visualisation de densité temporelle (quand ça a bardé)
aiderait le balayage. Actions rapides depuis la liste : ouvrir, marquer
comme vu/résolu (état local à la webapp).

### B. Détail d'un événement — la pièce maîtresse

L'écran qui doit gagner le concours. Sa mission : faire lire la
chronologie d'un incident comme un récit.

- **La chronologie** est l'élément central : un axe temporel vertical où
  se succèdent le déploiement lié (−3 min), les observations du contexte
  « avant », les signaux de l'anomalie, le contexte « après ». Logs en
  monospace avec leur niveau, métriques avec leur valeur, changements
  visuellement distincts.
- **Le panneau signaux** : chaque signal avec son score, son résumé et
  ses chiffres (valeur vs baseline). C'est ici que se joue la promesse
  « explicable » : l'opérateur doit pouvoir vérifier le calcul.
- **Les actions** : marquer le template en cause (`ignore` /
  `symptomatic` — c'est la boucle de feedback de tezcatl, l'action la
  plus utile de l'UI), copier le JSON brut, partager un lien, lancer
  l'analyse agent.
- **L'analyse agent** (voir §5) : un panneau ou un onglet où apparaît le
  compte rendu produit par l'agent via MCP — état vide avant analyse,
  en cours, résultat (texte structuré), avec la mention de ce que
  l'agent a consulté.

Cet écran doit exister en variante mobile : c'est lui qu'on ouvre depuis
la notification d'astreinte.

### C. Vue service

L'historique d'un service : timeline des événements et des déploiements
sur une période, pour répondre à « ce service est-il stable depuis la
v1.8.2 ? ». Accès aux templates appris du service.

### D. Templates et marquages

Le miroir de la commande `tezcatl templates` : la liste des templates
appris (partition, taille, marquage), triable, avec l'action de
marquage. C'est l'écran d'hygiène hebdomadaire, il peut être utilitaire
et dense.

### E. État du système

Petite page : santé de l'ingestion webhook (dernier événement reçu,
compteurs), configuration du MCP (URL, jeton), rien de plus.

## 5. Le serveur MCP (impact sur le design)

La webapp exposera un serveur MCP permettant à un agent externe (Claude
ou autre) d'analyser les événements. Outils prévus : `list_events`
(filtres), `get_event` (l'objet complet, contexte inclus),
`get_service_history`, `mark_template`. L'agent typique : « analyse
l'événement X, propose des hypothèses hiérarchisées, dis ce que tu as
vérifié ».

Conséquences pour les maquettes :

- l'analyse produite par l'agent est **attachée à l'événement** et
  persistée : prévoir son affichage (auteur, date, contenu markdown,
  hypothèses avec niveau de confiance de l'agent) ;
- distinguer visuellement, sans ambiguïté possible, **ce que le moteur a
  détecté** (déterministe, vérifiable) de **ce que l'agent suppose**
  (hypothèses). C'est un principe produit central : ne jamais mélanger
  les deux registres ;
- une action « analyser » avec état d'attente (l'analyse peut prendre
  dizaines de secondes).

## 6. Direction visuelle

- **Console d'opérateur, pas dashboard marketing.** Densité maîtrisée,
  hiérarchie typographique forte, pas de graphiques décoratifs. À
  regarder : la lisibilité de Linear, la sobriété de Healthchecks.io.
  Contre-exemples : les murs de widgets à la Grafana/Datadog.
- **Sombre par défaut** (usage nocturne), thème clair disponible.
  L'identité peut jouer le motif obsidienne : noirs profonds, un accent
  froid. Le nom « miroir » peut inspirer sans tomber dans le littéral.
- **La sévérité porte l'essentiel de la couleur** : 3 niveaux, jamais la
  couleur seule (icône ou libellé systématique, accessibilité).
- **Monospace pour les logs et templates**, avec les masques (`<NUM>`,
  `<IP>`, `<EMAIL>`) et wildcards (`<*>`) traités comme des tokens
  visuellement distincts du texte — c'est une signature possible de
  l'interface.
- **Le temps est le matériau premier** : heures relatives et absolues,
  décalages (« −3 min »), axes temporels. Soigner ces micro-formats.

## 7. Hors périmètre (ne pas maquetter)

- Édition de la configuration YAML de tezcatl.
- Graphiques de séries temporelles complets (pas un remplaçant de
  Grafana ; tout au plus des sparklines).
- Gestion d'utilisateurs/rôles (au plus un écran de connexion simple).
- Le pipeline d'ingestion lui-même (CLI existante).

## 8. Livrables attendus

1. Flux (desktop) avec ses filtres, y compris l'état « calme » (vide
   positif) et l'état rafale d'incident.
2. Détail d'événement (desktop **et** mobile), avec panneau d'analyse
   agent dans ses trois états (vide, en cours, résultat).
3. Vue service.
4. Templates/marquages.
5. État du système.

Priorité absolue : le détail d'événement. Si un seul écran doit être
exploré en profondeur avec des variantes, c'est lui.

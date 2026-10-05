# Livability Index — design

**Status:** design only. Nothing in this document is implemented.
**Date:** 2026-08-28

## Why this is a separate mode from `ranking`

`ranking` compares cities on indicators two portals happen to publish, and
deliberately emits no overall score. That refusal is correct for what `ranking`
is: crime, 311 responsiveness, and permit counts do not add up to anything, and
a composite of them would be an editorial weighting wearing arithmetic's
clothes.

This document designs the thing `ranking` declines to be — a livability index —
and the design problem is to build one without inheriting the dishonesty. The
answer is not to hide the weighting. It is to make the weighting an **input**,
and then to **measure how much of the result it drove**.

A ranking that survives every reasonable weighting is a finding. A ranking that
flips when you nudge the weights is also a finding, and the more common one.
Both are more useful than a confident integer.

## Three structural facts that shape everything below

**1. Within-city variance exceeds between-city variance.** Chicago publishes
life expectancy by community area (`qjr3-bm53`). The spread across Chicago
neighbourhoods is on the order of decades — far wider than the gap between any
two US cities' averages. A city-level mean is therefore close to useless for
"where should I live," and an index built only on means is measuring the wrong
thing. Every domain below is scored on **two** statistics: the median
neighbourhood and the 10th-percentile neighbourhood. How bad a bad
neighbourhood is tells you more than the average does.

**2. Livability is conditional on the resident.** What is good for someone
aged 25 without a car is not what is good for a family of four or a person with
a disability. A single universal ranking implies a universal resident who does
not exist. The index takes a **profile** and returns a ranking *given that
profile*; it never emits an unconditional ordering.

**3. Cost is a constraint, not an addend.** A city that scores well on
everything but is unaffordable to you is not livable for you, and no weighted
sum expresses that — adding a "cost" term simply lets a high amenity score buy
its way past unaffordability. Cost enters as a **gate** against the profile's
budget, applied before scoring.

## The criteria

Source tiers: `[csq]` reachable from these Socrata portals today (IDs verified
against the live catalog API on 2026-08-28); `[ext]` needs an external source;
`[none]` not reliably measurable at city scale.

### A. Housing and cost
| Indicator | Polarity | Source |
|---|---|---|
| Median rent, and rent as a share of median income | − | `[ext]` ACS B25064 / B25071 |
| Home price to income ratio | − | `[ext]` ACS / FHFA |
| Housing production per 1,000 residents | + | `[csq]` CHI `ydr8-5enu`, NYC `ipu4-2q9a` |
| Eviction filings per 1,000 renter households | − | `[csq]` NYC `6z8x-wfk4`; **Chicago publishes none** |
| Housing code violations per 1,000 units | − | `[csq]` NYC `wvxf-dwi5`, CHI `22u3-xenr` |
| Subsidised/affordable unit supply | + | `[csq]` CHI `yvj4-y3fb`, `s6ha-ppgi` |
| Homelessness rate | − | `[ext]` HUD PIT counts |

The eviction row is the design's first real test. Chicago publishes no eviction
dataset, so Chicago is **excluded from that indicator by name**, never scored as
zero. A city that does not publish its evictions has not thereby eliminated them.

### B. Health and environment
| Indicator | Polarity | Source |
|---|---|---|
| **Life expectancy, median and 10th-pct neighbourhood** | + | `[csq]` CHI `qjr3-bm53`; `[ext]` CDC PLACES elsewhere |
| Life expectancy gap by race/ethnicity | − | `[csq]` CHI `3qdj-cqb8` |
| PM2.5 and ozone exposure | − | `[csq]` CHI `rtmx-vkjr`, NYC `q68s-8qxv`; `[ext]` EPA AQS |
| Preventable hospitalisation rate | − | `[ext]` CDC PLACES |
| Primary care access | + | `[ext]` HRSA |
| Extreme heat days; tree canopy / heat island | −/+ | `[csq]` NYC `uvpi-gqnh`; `[ext]` NOAA |
| Water quality | + | `[csq]` CHI `qmqz-2xku` (beaches only — not drinking water) |

Life expectancy is the single best-validated livability indicator available: it
integrates income, healthcare, violence, pollution, and stress into one number
that is hard to game and is published at neighbourhood resolution.

### C. Safety
| Indicator | Polarity | Source |
|---|---|---|
| Violent offences per 1,000 | − | `[csq]` CHI `ijzp-q8t2`, NYC `qgea-i56i` |
| Property offences per 1,000 | − | `[csq]` same |
| **Traffic deaths and serious injuries per 100k** | − | `[csq]` CHI `85ca-t3if`, NYC `h9gi-nx95` |
| Pedestrian/cyclist injury rate | − | `[csq]` CHI `u6pd-qa9d`, NYC `f55k-p6yu` |
| Fire/EMS response time | − | `[ext]` NFIRS |

Traffic deaths belong here at equal footing with crime and are usually omitted.
In many US cities they exceed homicides, they are far less confounded by
reporting practice, and unlike reported crime they are close to a true count.
Including them materially changes which cities look safe.

### D. Mobility
| Indicator | Polarity | Source |
|---|---|---|
| Mean commute time; share of commutes over 60 min | − | `[ext]` ACS B08303 |
| Transit mode share | + | `[ext]` ACS B08301 |
| Population within 400m of frequent transit | + | `[ext]` GTFS |
| Cost of car ownership as share of income | − | `[ext]` BLS CEX |
| Intersection density / walkability | + | `[ext]` OSM |

Almost nothing here is on a Socrata portal. Transit agencies are separate
authorities: NYC's portal returns no subway ridership at all, because that is
MTA's. Any honest livability index needs GTFS and ACS, which means csq needs a
non-Socrata reference-data path.

### E. Opportunity and equity
| Indicator | Polarity | Source |
|---|---|---|
| Intergenerational income mobility | + | `[ext]` Opportunity Atlas |
| Residential segregation (dissimilarity index) | − | `[ext]` ACS |
| Unemployment; median household income | −/+ | `[ext]` BLS / ACS |
| **Neighbourhood spread on every other indicator** | − | derived |
| Racial disparity in service delivery / enforcement | − | `[csq]` derived from 311 + police data |

The derived spread indicator is the one most rankings lack. A city whose worst
neighbourhood is close to its best is a materially different place to live from
one with the same average and a chasm, and the difference is invisible to any
index built on means.

### F. Services and governance
| Indicator | Polarity | Source |
|---|---|---|
| 311 median days to close | − | `[csq]` CHI `v6vf-nfxy`, NYC `erm2-nwe9` |
| **Variance in 311 response across neighbourhoods** | − | `[csq]` derived |
| Restaurant inspection pass rate | + | `[csq]` CHI `4ijn-s7e5`, NYC `43nn-pn8j` |
| Contract concentration / lobbying intensity | − | `[csq]` `corruption` mode |
| Road and sidewalk condition | + | `[csq]` NYC `6kbp-uz6m` |

Response *variance* is a better governance measure than response *median*: it
asks whether the city serves its neighbourhoods equally, which a median hides
entirely.

### G. Amenity and social
| Indicator | Polarity | Source |
|---|---|---|
| Park access (share within 10-min walk) | + | `[csq]` CHI `ej32-qgdr`, NYC `enfh-gkve` |
| Businesses per 1,000 (food, retail, venues) | + | `[csq]` CHI `r5kz-chrr` |
| Library and community facility access | + | `[csq]` both |
| Voter turnout / civic participation | + | `[ext]` state boards |
| Childcare cost and availability | − | `[ext]` |
| School quality | + | `[ext]` state DOE |

### H. Explicitly unscored
311 request volume, business churn, and population growth get **polarity
`none`**. Each is genuinely ambiguous — high 311 volume means worse conditions
*or* better reporting channels, and nothing in the data separates them. These
are reported descriptively and never enter the score. Refusing to score an
ambiguous indicator is what keeps the rest of the score meaningful.

## The algorithm

**0. Profile.** Take a resident profile: budget, household composition, car
availability, mobility needs, and a domain weight vector `w` over A–G summing
to 1. There is no default profile; the caller supplies one, because there is no
default resident.

**1. Affordability gate.** Drop any city where modelled housing cost for the
profile's household exceeds its budget. Gated cities are listed by name with the
figure that excluded them. This runs *before* scoring, so amenity cannot buy
past unaffordability.

**2. Eligibility gate.** A city enters only with a cited population and at least
τ = 0.7 of the total weight actually covered by published data. Excluded cities
are named with the reason.

**3. Neighbourhood resolution.** For each indicator, compute the median and the
10th-percentile neighbourhood value. Both carry forward; a domain's score is
`0.5·median + 0.5·p10`, which prices in the floor rather than only the centre.

**4. Robust normalisation.** For indicator *j*, city *i*:

```
z_ij = (x_ij − median_j) / (1.4826 · MAD_j)     clipped to [−3, +3]
s_ij = polarity_j · z_ij                        polarity none → excluded
```

MAD, not standard deviation, so one outlier city cannot rescale everyone; the
clip bounds the residual damage.

**5. Coverage-renormalised composite.** With `A_i` the indicators available for
city *i*:

```
S_i = ( Σ_{j∈A_i} w_j · s_ij ) / ( Σ_{j∈A_i} w_j )
```

Dividing by the weight actually covered stops a city being penalised for what
its portal fails to publish — a data-availability artefact, not a quality of the
place.

**6. Dominance backbone.** City A dominates B if A is at least as good on every
scored indicator and strictly better on one. Dominance holds under **all**
weightings, so these are the only claims that survive any editorial choice.
Report the partial order separately, as fact.

**7. Weight sensitivity.** Do not report a rank; report a rank interval. Sample
10,000 weight vectors from `Dirichlet(κ·w)` around the declared weights,
recompute the ranking each time, and record the distribution:

```
rank_i → median, 5th pct, 95th pct
```

Publish a point rank only when the 90% interval has width ≤ 1. Otherwise publish
the interval and state that the data does not determine the ordering.

**8. Report.** Emit, together and inseparably: the rank intervals, the dominance
order, the gated and excluded cities with reasons, the per-domain contributions,
and every caveat attached to every dataset touched.

## Known limits

- **Below ~5 cities the composite is degenerate.** Two cities give `z = ±0.707`
  on every indicator regardless of the size of the gap, and sensitivity analysis
  has nothing to vary. With Chicago and NYC alone, run step 6 only — dominance
  and raw paired differences — and skip scoring. More bindings are a
  prerequisite, not an enhancement.
- **Reported crime remains publishing practice as much as conditions.** Traffic
  deaths and life expectancy are included partly because they are far less
  sensitive to how hard a city looks.
- **Roughly half the criteria are not on Socrata at all.** Mobility and
  opportunity are almost entirely external. An honest index requires csq to
  acquire a non-Socrata reference-data path; without it, this index measures the
  subset of livability that municipal open-data portals happen to cover, which
  is not the same thing and must never be labelled as though it were.
- **A city index cannot answer "where should I live."** It can answer "which of
  these places is better on criteria I have named and weighted, and how much
  does that answer depend on my weighting." That is a smaller claim, and it is
  the one the data supports.

# Labelling guide (gold set)

This guide is for the native speaker labelling SecureBrand's gold set. Their labels are the "correct answers" every AI model is measured against, so consistency matters more than speed.

- **Amount:** about 1,000 comments, 200 posts and 100 images, roughly 3-4 working days.
- **Tool:** the labelling page (`sb-eval label`). It shows one item at a time and saves after every item.
- **Privacy:** the texts are real public comments by real people. Do not copy them anywhere, do not take screenshots, and do not share them. Everything stays on SecureBrand's server.

## How to work

1. Read the **context** first, if shown: the page name, the post the comment replies to, and the comment it replies to. Then read the **item** (the yellow box). Label only the item; the context only helps you understand it.
2. Fill in every section, top to bottom, then press **Save and next**.
3. If you cannot tell (the text is unclear, in a language you do not know, or you would only be guessing), tick **unsure** and write why in the notes. Unsure items are reviewed, not scored.
4. Take breaks. After every 100 items, re-read this guide's examples.
5. The first 50 items will be checked together with SecureBrand, to align on difficult cases before you continue.

## 1. Language

| Choice | When |
|---|---|
| `ar` | Any Arabic: Iraqi dialect, Modern Standard Arabic, other dialects, **and Arabizi** (Arabic written in Latin letters, e.g. "shlonak", "3ashet edak"). |
| `ckb` | Kurdish Sorani (Arabic script, letters such as ڕ ڵ ۆ ێ ە). |
| `kmr` | Kurdish Kurmanji (Latin script, letters such as ç ş ê î û). |
| `en`, `tr`, `fa` | English, Turkish, Persian. |
| `other` | Any other language. |
| `none` | No words at all: only emoji, stickers or symbols. |

If a comment mixes languages, choose the main one (most of the words).

**Dialect** (only for Arabic): `iraqi`, `gulf`, `levantine`, `egyptian`, `msa` (formal Arabic, typical of news posts), `other`, or `none` (too short to tell, e.g. "شكرا").

## 2. Overall sentiment of the author

Ask: **what does the author feel or think, about the subject they are talking about?** Do not judge the news itself.

| Label | Meaning | Examples |
|---|---|---|
| `positive` | Approves, praises, thanks, is happy | "عاشت ايدكم", "خوش خدمة", "الله يحفظكم" (to the page) |
| `negative` | Complains, criticises, insults, is angry or disappointed | "شبكتكم زبالة", "كل يوم انقطاع", "حرامية" |
| `neutral` | Asks, informs, tags people, prays, gives condolences, no opinion | "شكد السعر؟", "@Ali", "الله يرحمه", "تم" |
| `mixed` | Both a clear positive and a clear negative | "العرض حلو بس الشبكة تعبانة" |

**Rules for hard cases:**
- **Sarcasm:** label what the author *means*, and tick **sarcastic**. "خوش شبكة 👏😂" under an outage post is **negative** + sarcastic.
- **Condolences and prayers** ("الله يرحمه", "انا لله وانا اليه راجعون") are **neutral**. Their feeling goes in emotion (sadness).
- **Blessings to the page or brand** ("الله يوفقكم", "مبروك") are **positive**.
- **Emoji only:** 😂 under a funny post is positive; 😂 under an announcement of a price rise is likely sarcastic negative; 😡 is negative; ❤️ is positive. If you cannot tell from the context, choose neutral.
- **News posts** (formal Arabic) usually state facts: **neutral**, unless the wording clearly takes a side.
- **Giveaway entries** ("اني اريد اربح", tagging three friends) are **neutral**.

## 3. Entities and the sentiment toward each

List the **brands, products, organisations, public persons, positions, places and events** that the item names or clearly refers to. For each one:

- **Name as written** (copy it from the text).
- **Type:**

| Type | Examples |
|---|---|
| `brand` | Asiacell, Zain Iraq, Korek, Qi Card, Toters |
| `product` | iPhone 15, a named package or offer |
| `organization` | ministries, banks, parliament, a university |
| `person_public` | politicians, officials, clerics, artists, journalists, athletes |
| `position` | an office, whoever holds it: "رئيس الوزراء" (Prime Minister of Iraq), "وزير الكهرباء", "محافظ البصرة". If the person is named too, list both. |
| `place` | Baghdad, Basra, a hospital by name |
| `event` | a festival, a match, a named campaign |
| `media_outlet` | a TV channel or news site |
| `other` | anything else worth tracking |

- **Registry id:** start typing and pick from the list if the entity is there (e.g. `asiacell`, `zain-iraq`). Leave it empty if it is not listed.
- **Sentiment toward this entity**, which can differ from the overall sentiment. "Asiacell أحسن من زين" is positive toward Asiacell and negative toward Zain.
- **About** (aspect): what the sentiment is about: price, quality, network, internet, customer_service, delivery, billing, app, staff, availability, offers, reliability, safety, management, general, other.

**Implicit mentions:** on a brand's own page, "شبكتكم", "انتم" and "خدمتكم" refer to that brand. Add the brand as an entity.

**Never list private persons**: friends tagged in a comment, ordinary people named in a comment. They are not entities (privacy). Leave them out entirely.

Up to six entities per item; most comments have none or one.

## 4. Topics (every one that applies)

Pick **every** topic from the list that the item is about, whether it says it directly or only implies it ("الوطنية طافية" is about electricity supply), the most specific ones (it is grouped by area, with Arabic names; some topics have sub-topics, shown indented). Most items have one to three. If nothing fits, pick `other` and write the missing topic in the notes (for example "missing topic: water shortage"). Brands, people and places are entities, not topics: they become topics automatically.

Examples:
- "النت واقف من الصبح": `telecom.outage`.
- "شكد سعر الباقة الشهرية؟": `telecom.pricing`.
- "الكهرباء تجي ساعتين باليوم": `services.electricity`.
- "شوكت تنزل الرواتب": `economy.salaries`.

## 5. Signals

**Intent**, what the author is doing:

| Intent | When |
|---|---|
| `complaint` | Reports a problem, criticises |
| `praise` | Thanks, compliments |
| `question` | Asks for information |
| `request` | Asks for an action ("صلحوا الشبكة") |
| `suggestion` | Proposes an improvement |
| `purchase_intent` | Wants to buy or subscribe |
| `churn_threat` | Says they will leave ("راح ابدل لزين") |
| `recommendation` | Advises others to use or avoid something |
| `information` | States facts or news |
| `giveaway_entry` | Enters a contest |
| `greeting_religious` | Greetings, prayers, condolences |
| `tagging` | Only tags people |
| `spam_ad` | Advertises something unrelated |
| `other` | Anything else |

**Emotion:** anger, joy, sadness, fear, surprise, disgust, trust, anticipation, or none.

**Toxicity:** `none`; `mild` (insults, swearing, vulgar words); `severe` (hate speech against a group, threats of violence, incitement).

**Spam:** ads for unrelated things, scams, fake prizes, selling numbers or followers, link spam.

**Urgent:** the item reports danger, an outage affecting many people, fraud or a scam, a health or food-safety problem, or an accident. In short, something a brand or an authority should see within the hour.

**Advertising** (mainly posts and images; for comments use spam), one choice:

| Choice | When |
|---|---|
| `none` | Not an ad |
| `affiliate` | Affiliate marketing: links or codes that earn the poster a commission ("استخدم كودي", "الرابط بالبايو", Temu, Shein or AliExpress codes) |
| `own` | A brand, shop or business promoting its own products, services or offers |
| `paid` | Disclosed paid promotion of another brand ("إعلان", "برعاية", "sponsored") |
| `hidden` | Reads like paid promotion but is not marked as such: an influencer or news page praising a product with prices, discount codes, links or "order now" |
| `sale` | A private person selling or buying something (a car, a phone, a flat) |

**Possible legal or policy breach**: tick every one that applies, only when the item itself does it (not when it reports or condemns it):

| Category | When |
|---|---|
| `hate_speech` | Attacks or dehumanises people for their sect, ethnicity, religion, tribe, nationality, gender or disability |
| `threat` | Threatens someone with harm (including tribal threats) |
| `incitement` | Calls for violence against anyone, for revenge or tribal attacks, or for hatred against a sect, ethnicity, religion, tribe or nationality |
| `terrorism` | Praises, supports or spreads propaganda for terrorist or armed extremist groups |
| `child_safety` | Sexualises, abuses or exploits children |
| `sexual_content` | Explicit sexual content, nudity, offers of sexual services |
| `graphic_violence` | Gore, or celebrates injury or killing |
| `self_harm` | Encourages suicide or self-harm |
| `drugs` | Sells, buys or promotes illegal drugs |
| `weapons` | Sells or buys weapons or ammunition |
| `gambling` | Promotes betting or gambling |
| `regulated_goods` | Sells alcohol, prescription medicines without prescription, or other restricted goods |
| `fraud_scam` | Scams, phishing, fake giveaways, fake jobs, pyramid schemes, a fake page pretending to be a brand or official |
| `unlicensed_finance` | Currency trading, investment or loan offers promising returns |
| `counterfeit` | Fake branded goods, pirated content, accounts for sale |
| `privacy` | Publishes someone's phone number, address, ID, private photos or messages |
| `defamation` | Accuses a named person (officials included) or company of a crime or serious wrongdoing, as a fact, without evidence or a source |
| `misinformation` | States as fact something false or very likely false that can cause harm or panic (health, security, disasters, elections, shortages, the dollar rate, fake quotes or documents). Only clear cases |
| `extortion` | Blackmail, threatening to publish private photos or information unless paid |
| `religious_offense` | Insults, mocks or attacks a religion, sect, sacred text, prophet, imam, saint, religious symbol or rite, or a religious figure as a religious figure. Always tick it |
| `other_illegal` | Clearly illegal in Iraq and not listed above: trafficking, bribery offers, vote buying, selling official documents or exam questions, organ trade |

Criticism, opinions, satire and complaints are not breaches by themselves; tick a category only when they also do it (criticism that states an unproven crime as fact is defamation; mocking a sacred figure is religious_offense). Not breaches by themselves: peaceful political opinion; a respectful religious question, disagreement, or someone stating their own belief or doubt; criticism of a cleric's political actions; a person's sexuality or gender identity.

## 6. Images (only when an image is shown)

- **Logos:** the brands whose logo, sign or packaging is visible (registry ids if listed, otherwise names), separated by commas.
- **Words that must be read from the image:** two to five important words written in the image (a price, a brand name, a slogan), separated by commas. They check whether the AI reads text in images.
- Fill in sentiment, entities and topics for **the message of the image**. Never name a person from their face; only if their name is written in the image.

## Worked examples

| Item (context) | Labels |
|---|---|
| "شبكة اسياسيل خايسة النت واقف من الصبح" (Asiacell's page) | ar / iraqi; **negative**; entity اسياسيل · brand · asiacell · negative · network; topic telecom.outage; complaint; anger; urgent (outage) |
| "خوش عرض بس كورك احسن" (Asiacell's page, offer post) | **mixed**; entity كورك · brand · korek · positive · general; topic telecom.pricing; recommendation |
| "الله يرحمه ويصبر اهله" (news of a death) | **neutral**; no entities; topic religion.condolences; greeting_religious; sadness |
| "خوش كهرباء 👏😂" (ministry announces better supply) | **negative** + sarcastic; entity وزارة الكهرباء if named in the post and clearly meant · negative · reliability; topic services.electricity; complaint |
| "@Ali Hassan @Sara" (giveaway post) | **neutral**; no entities (private persons); topic brand.giveaway; tagging |
| "ربح ايفون مجاني اضغط الرابط" | neutral; topic technology.scams; spam_ad; spam |
| "سڵاو، کەی ئینتەرنێت چاک دەبێتەوە؟" | ckb; dialect none; neutral; topic telecom.internet; question |

## Quality checks we will run

- About 10% of items will be labelled twice (by you, a week later, without seeing your first answer) to measure consistency. Disagreement shows where this guide needs to be clearer, not that you made a mistake.
- If a second labeller is available, their agreement with you (Cohen's kappa) will be reported next to the model scores.

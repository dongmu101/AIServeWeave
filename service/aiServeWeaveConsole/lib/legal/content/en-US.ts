import type { LegalEntity } from "../entity.ts";
import type { LegalSet } from "../types.ts";

/**
 * enUS is the authoritative English text. The other languages are
 * translations of it; when they differ, the English text prevails (stated in
 * each translation's introduction).
 *
 * enUS 是具权威性的英文正文。其他语言是它的译本；不一致时以英文为准（各译本的开头都有说明）。
 */
export function enUS(e: LegalEntity): LegalSet {
  const svc = e.serviceName;
  const contact = `${e.company}, ${e.address}. Email: ${e.emailText}`;
  const creditRefund = e.refundWindowDays > 0
    ? [
        `You may request a refund of a credit package within ${e.refundWindowDays} days of the purchase date, for the credits from that purchase that remain unused.`,
        "The refund amount is proportional to the unused credits of that purchase, based on the price you paid. Credits that have already been consumed are not refundable.",
        `To request a refund, email ${e.emailText} with your account email and the order ID.`,
        "We review requests and normally respond within 5 business days. While a request is being processed, the corresponding credits are held and cannot be spent.",
      ]
    : [
        "Credit packages are non-refundable once the credits have been added to your balance, except for duplicate or erroneous charges (see below) or where a refund is required by applicable law.",
      ];

  return {
    terms: {
      title: "Terms of Service",
      intro: [
        `These Terms of Service ("Terms") govern your access to and use of ${svc} (the "Service"), operated by ${e.company} ("we", "us" or "our"). By creating an account, making a purchase or using the Service, you agree to these Terms. If you use the Service on behalf of an organization, you confirm that you are authorized to accept these Terms for it.`,
      ],
      sections: [
        {
          heading: "The Service",
          blocks: [
            `${svc} is an AI model access platform. It provides:`,
            [
              "an API gateway that lets you call AI models through a unified API, including chat, embeddings, reranking and image-generation workflows;",
              "prepaid credits, used to pay for API usage at the per-token model prices published on our Pricing page; and",
              "dedicated monthly plans, which reserve Claude Code or Codex capacity for your exclusive use for one month.",
            ],
            `Claude Code, Codex and other model or product names are trademarks of their respective owners. ${svc} is an independent service and is not affiliated with or endorsed by them.`,
          ],
        },
        {
          heading: "Eligibility and accounts",
          blocks: [
            "You must be at least 18 years old and able to enter into a binding contract. You must give accurate registration information and keep it up to date.",
            "You are responsible for keeping your password and API keys confidential and for all activity under your account. Tell us promptly if you suspect unauthorized use.",
          ],
        },
        {
          heading: "Products, prices and payment",
          blocks: [
            "Our current products and prices are listed on the Pricing page, in the currency shown there. Applicable taxes are calculated and shown at checkout.",
            "Payments are processed by our third-party payment partners, which may act as the merchant of record or reseller for your purchase. Their terms also apply to the payment. We do not receive or store your full card number.",
            "An order is complete only when the payment partner confirms the payment. Credits and plans are delivered after that confirmation.",
            [
              "Prepaid credits are added to your balance and deducted as you use the Service, at the model prices in effect at the time of use. Credits have no cash value, cannot be transferred, and can be used only on the Service. We may change model prices for future usage by publishing new prices on the Pricing page.",
              "A dedicated monthly plan gives one month of access, counted by calendar month in UTC from activation. Buying again before expiry extends the plan by one month from the current expiry date. Plans do not renew automatically.",
              "A pre-sale plan is activated when we allocate capacity to you; the month starts at allocation, not at payment.",
            ],
          ],
        },
        {
          heading: "Acceptable use",
          blocks: [
            "You agree not to use the Service to:",
            [
              "break any law, or create or distribute content that is illegal, infringing, defamatory, or that sexually exploits minors;",
              "distribute malware, attack or probe systems, or send spam;",
              "harass, deceive or harm others, including by impersonation or fraud;",
              "violate the usage policies of the underlying model providers;",
              "circumvent quotas, rate limits, billing or security measures, or interfere with the Service or other users; or",
              "share account credentials or API keys outside your organization.",
            ],
          ],
        },
        {
          heading: "Model output and your content",
          blocks: [
            "Output is generated by third-party AI models. It may be inaccurate, incomplete or offensive, and you are responsible for evaluating it before relying on it.",
            "You keep all rights to the inputs you submit and, as between you and us, to the output you receive. You grant us the limited permission needed to process them in order to provide the Service. We do not use your content to train models. Our Privacy Policy explains how we handle data.",
          ],
        },
        {
          heading: "Availability",
          blocks: [
            "We work to keep the Service available but do not guarantee uninterrupted operation unless we agree to a service level in writing. We may carry out maintenance, apply quotas and rate limits, and change or discontinue features. We will give reasonable notice of material changes that affect paid plans.",
          ],
        },
        {
          heading: "Suspension and termination",
          blocks: [
            "You may stop using the Service at any time and may ask us to close your account.",
            "We may suspend or close your account if you breach these Terms, fail to pay, engage in fraud or abuse, or if the law requires it. Refunds, if any, are handled under our Refund Policy.",
          ],
        },
        {
          heading: "Refunds",
          blocks: ["Refunds are governed by our Refund Policy, which forms part of these Terms."],
        },
        {
          heading: "Disclaimers",
          blocks: [
            "To the extent permitted by law, the Service is provided \"as is\" and \"as available\", without warranties of any kind, including fitness for a particular purpose, non-infringement and accuracy of output.",
          ],
        },
        {
          heading: "Limitation of liability",
          blocks: [
            "To the extent permitted by law, we are not liable for indirect, incidental, special, consequential or punitive damages, or for loss of profits, data or goodwill. Our total liability for any claim relating to the Service is limited to the amount you paid us in the 12 months before the event giving rise to the claim.",
          ],
        },
        {
          heading: "Indemnity",
          blocks: [
            "You will indemnify us against third-party claims arising from your content or your breach of these Terms, to the extent permitted by law.",
          ],
        },
        {
          heading: "Changes to these Terms",
          blocks: [
            "We may update these Terms. We will post the new version on this page and, for material changes, give reasonable advance notice by email or in the Console. Continuing to use the Service after the change takes effect means you accept the updated Terms.",
          ],
        },
        {
          heading: "Governing law",
          blocks: [
            `These Terms are governed by the laws of ${e.jurisdiction}. Disputes will be resolved by the competent courts of ${e.jurisdiction}. Nothing in these Terms limits rights you have under mandatory consumer protection laws.`,
          ],
        },
        {
          heading: "Contact",
          blocks: [contact],
        },
      ],
    },

    privacy: {
      title: "Privacy Policy",
      intro: [
        `This Privacy Policy explains how ${e.company} ("we", "us") collects, uses and shares personal information when you use ${svc} (the "Service"). We are responsible for this information as the data controller.`,
      ],
      sections: [
        {
          heading: "Information we collect",
          blocks: [
            [
              "Account information: email address, display name, organization name, and your password, which we store only as a one-way hash.",
              "Billing information: order records such as product, amount, currency, time and status, together with the payment partner's references. Card and payment account details are collected directly by our payment partners; we do not receive or store your full card number.",
              "Usage and log data: API request metadata such as time, model, token counts, status, latency, request ID and IP address; credit usage; and administrative audit events such as sign-ins and API key changes.",
              "Content you submit: prompts, files and workflow inputs, and the output generated from them. We process this content to fulfil your requests. We do not record the full content of prompts or model responses in our logs. Output of asynchronous jobs (for example generated images) is stored so that you can download it.",
              "Cookies: only strictly necessary cookies, namely an encrypted session cookie that keeps you signed in and a cookie that remembers your language. We do not use advertising or third-party analytics cookies.",
            ],
            "API keys are stored only as a one-way hash and are shown to you in full once, when created.",
          ],
        },
        {
          heading: "How we use information",
          blocks: [
            [
              "to provide, operate and secure the Service, including authentication;",
              "to process payments, keep billing records and prevent fraud;",
              "to provide support and send notices about the Service and your account; and",
              "to comply with legal obligations.",
            ],
            "We do not sell your personal information, and we do not use your content to train AI models.",
          ],
        },
        {
          heading: "How we share information",
          blocks: [
            [
              "Payment partners, which process payments and may act as merchant of record for your purchase.",
              "AI model and compute providers, to which your requests are forwarded so they can be fulfilled. Their own policies apply to the data they receive.",
              "Hosting and infrastructure providers that operate the Service on our behalf.",
              "Authorities, when the law requires it, and a successor in a merger or acquisition, subject to this Policy.",
            ],
          ],
        },
        {
          heading: "International transfers",
          blocks: [
            "We and our providers may process information in countries other than yours. Where required, we use appropriate safeguards for these transfers.",
          ],
        },
        {
          heading: "Retention",
          blocks: [
            [
              "Account information: while your account is active, and afterwards as long as needed to meet legal obligations.",
              "Request metadata logs: about 30 days by default.",
              "Usage records used for billing and statements: about 400 days by default.",
              "Billing records: as long as tax and accounting laws require.",
              "Job output: until you delete it or the applicable retention period ends.",
            ],
          ],
        },
        {
          heading: "Security",
          blocks: [
            "We protect information with measures including encryption in transit (HTTPS), encrypted session cookies, hashed passwords and API keys, and access controls. No system is completely secure, so please tell us promptly about any suspected incident.",
          ],
        },
        {
          heading: "Your rights",
          blocks: [
            `Depending on where you live, you may have the right to access, correct, delete or export your personal information, and to object to or restrict certain processing. To make a request, email ${e.emailText}. We respond within 30 days, and we may need to verify your identity first. You may also complain to your local data protection authority.`,
          ],
        },
        {
          heading: "Children",
          blocks: ["The Service is not directed to anyone under 18, and we do not knowingly collect their information."],
        },
        {
          heading: "Changes to this Policy",
          blocks: ["We may update this Policy. We will post the new version on this page and give notice of material changes by email or in the Console."],
        },
        {
          heading: "Contact",
          blocks: [contact],
        },
      ],
    },

    refund: {
      title: "Refund Policy",
      intro: [
        `This Refund Policy explains when purchases on ${svc} can be refunded. It forms part of our Terms of Service. It does not limit any rights you have under mandatory consumer protection laws.`,
      ],
      sections: [
        { heading: "Prepaid credit packages", blocks: creditRefund },
        {
          heading: "Dedicated monthly plans",
          blocks: [
            "Because capacity is reserved for you as soon as a plan is activated, an activated plan is not refundable, including for the unused part of a month.",
            "If we cannot activate a plan after you have paid for it, for example because the capacity is no longer available, we refund the payment in full.",
          ],
        },
        {
          heading: "Pre-sale plans",
          blocks: [
            `You may cancel a pre-sale order that has not yet been delivered by emailing ${e.emailText}, and we will refund it in full. If we are unable to deliver a pre-sale order, we refund it in full. Once capacity has been allocated, the plan is treated as an activated monthly plan.`,
          ],
        },
        {
          heading: "Purchases paid with Alipay prepaid quota",
          blocks: [
            `Credits or plans paid for with Alipay prepaid quota cannot be reversed automatically. Contact us at ${e.emailText} and we will review your request case by case.`,
          ],
        },
        {
          heading: "Duplicate or erroneous charges",
          blocks: ["If you were charged twice or charged in error, contact us and we will refund the incorrect charge in full."],
        },
        {
          heading: "Non-refundable items",
          blocks: [
            [
              "credits that have already been consumed;",
              "free, promotional or granted credits; and",
              "purchases on accounts closed for breach of our Terms of Service or for fraud, except where the law requires otherwise.",
            ],
          ],
        },
        {
          heading: "How refunds are paid",
          blocks: [
            "Approved refunds are returned to the original payment method through the payment partner that processed the purchase. Depending on the payment partner and your bank, the money usually arrives within 5–10 business days.",
          ],
        },
        {
          heading: "Chargebacks",
          blocks: [
            "Please contact us before filing a chargeback or payment dispute, so we can resolve the issue quickly. While a dispute is open, we may suspend the related credits or plan.",
          ],
        },
        {
          heading: "Contact",
          blocks: [contact],
        },
      ],
    },
  };
}
